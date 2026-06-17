package keypool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// messagesURLFromBase derives the Anthropic messages endpoint from the configured upstream URL.
func messagesURLFromBase(baseURL string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, segment := range segments {
		if segment == "v1" {
			u.Path = "/" + strings.Join(segments[:i+1], "/")
			return strings.TrimRight(u.String(), "/") + "/messages"
		}
	}

	return strings.TrimRight(u.String(), "/") + "/v1/messages"
}

// messagesCountTokensURLFromBase derives the Anthropic count_tokens endpoint.
func messagesCountTokensURLFromBase(baseURL string) string {
	return messagesURLFromBase(baseURL) + "/count_tokens"
}

// Messages proxies POST /v1/messages to the upstream Anthropic-compatible API.
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "Only POST method is allowed", "invalid_request_error")
		return
	}

	// Validate proxy key
	if !h.validateProxyAuth(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Invalid or missing API key"}}`))
		return
	}

	start := time.Now()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		writeAnthropicError(w, http.StatusNotFound, "Channel not found", "not_found_error")
		return
	}

	// Save the original model name before any mapping/replacement (for logging).
	originalModel := extractModel(body)

	// Check model mapping: if the request model is a mapping alias, swap model and channel.
	modelMapped := false
	mappedModelName := ""
	if mappedChannelID, mappedModel, ok := h.pool.ResolveModelMapping(originalModel); ok && mappedModel != "" {
		if mc, merr := h.pool.GetChannelByID(mappedChannelID); merr == nil && mc.Enabled {
			mappedModelName = mappedModel
			body = replaceModel(body, mappedModel)
			channel = mc
			modelMapped = true
		}
	} else if mm := h.pool.GetModelMappingByName(originalModel); mm != nil && !mm.Enabled {
		// Model name matches a disabled mapping — return error directly.
		writeAnthropicError(w, http.StatusBadRequest, fmt.Sprintf("Model mapping '%s' is disabled", originalModel), "invalid_request_error")
		return
	}
	log.Printf("[messages] originalModel=%s mappedModel=%s channel=%s(%d) modelMapped=%v", originalModel, mappedModelName, channel.Name, channel.ID, modelMapped)

	key, defaultModel, err := h.pool.GetKey(channel.ID)
	if err != nil {
		writeAnthropicError(w, http.StatusServiceUnavailable, "No API key available: "+err.Error(), "api_error")
		return
	}

	// Swap model if needed (reuse same logic).
	// Skip swapModelIfNeeded when a model mapping was applied to preserve the mapped target model.
	if !modelMapped {
		var models []string
		models = h.getCachedModels(channel.ID)
		if models == nil {
			models = h.refreshModels(channel.ID, modelsURLFromBase(channel.BaseURL), key)
		}
		body = h.swapModelIfNeeded(body, models, defaultModel)
	}

	upstreamURL := messagesURLFromBase(channel.BaseURL)
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "Failed to create request", "api_error")
		return
	}

	req.Header.Set("Content-Type", "application/json")
	// Anthropic uses x-api-key header
	req.Header.Set("x-api-key", key)
	// anthropic-version header
	if av := r.Header.Get("anthropic-version"); av != "" {
		req.Header.Set("anthropic-version", av)
	} else {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	h.prepareUpstreamRequest(req)

	client, cerr := h.upstreamClientForChannel(channel, 120*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
	resp, err := client.Do(req)
	if err != nil {
		h.recordKeyFailure(key)
		writeAnthropicError(w, http.StatusBadGateway, "Failed to proxy request: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	// Forward response headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	proxyKey := extractProxyKey(r)
	// Use the original model name (before mapping) for logging.
	model := originalModel

	// keyOK tracks whether the key should be considered healthy.
	keyOK := resp.StatusCode == http.StatusOK
	isBadKey := resp.StatusCode == 401 || resp.StatusCode == 403

	isStreaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	if isStreaming {
		var promptTokens, completionTokens, totalTokens int
		var streamBuf bytes.Buffer
		flusher, _ := w.(http.Flusher)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			w.Write(line)
			w.Write([]byte("\n"))
			if flusher != nil {
				flusher.Flush()
			}
			streamBuf.Write(line)
			streamBuf.WriteByte('\n')
			if bytes.HasPrefix(line, []byte("event: error")) {
				keyOK = false
			}
			if bytes.HasPrefix(line, []byte("event: message_start")) {
				// Next line should be data with usage
			}
			if bytes.HasPrefix(line, []byte("data: ")) && !bytes.Contains(line, []byte("[DONE]")) {
				var chunk map[string]interface{}
				if json.Unmarshal(line[6:], &chunk) == nil {
					if _, hasErr := chunk["error"]; hasErr {
						keyOK = false
					}
					if usage, ok := chunk["usage"].(map[string]interface{}); ok {
						if v, ok := usage["input_tokens"].(float64); ok {
							promptTokens = int(v)
						}
						if v, ok := usage["output_tokens"].(float64); ok {
							completionTokens = int(v)
						}
					}
					if delta, ok := chunk["delta"].(map[string]interface{}); ok {
						if usage, ok := delta["usage"].(map[string]interface{}); ok {
							if v, ok := usage["output_tokens"].(float64); ok {
								completionTokens = int(v)
							}
						}
					}
				}
			}
		}
		if !keyOK {
			h.recordKeyFailure(key)
			if isBadKey && channel.KeyMode == "failover" {
				h.pool.RotateFailoverKey(channel.ID, key)
			}
		} else {
			h.resetKeyFailures(key)
		}
		totalTokens = promptTokens + completionTokens
		h.pool.IncrementUsage(key, promptTokens, completionTokens, totalTokens)
		go h.pool.LogRequest(&RequestLog{
			Method: "POST", Path: "/v1/messages", StatusCode: resp.StatusCode,
			LatencyMs: time.Since(start).Milliseconds(), ProxyKey: proxyKey, UpstreamKey: truncKey(key, 8),
			Model: model, PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens,
			RequestBody: string(body), ResponseBody: streamBuf.String(),
		})
	} else {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var promptTokens, completionTokens, totalTokens int
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var parsed map[string]interface{}
			if json.Unmarshal(bodyBytes, &parsed) == nil {
				if _, hasErr := parsed["error"]; hasErr {
					keyOK = false
				}
				if t, _ := parsed["type"].(string); t == "error" {
					keyOK = false
				}
				if usage, ok := parsed["usage"].(map[string]interface{}); ok {
					if v, ok := usage["input_tokens"].(float64); ok {
						promptTokens = int(v)
					}
					if v, ok := usage["output_tokens"].(float64); ok {
						completionTokens = int(v)
					}
				}
			}
		}
		if !keyOK {
			h.recordKeyFailure(key)
			if isBadKey && channel.KeyMode == "failover" {
				h.pool.RotateFailoverKey(channel.ID, key)
			}
		} else {
			h.resetKeyFailures(key)
		}
		totalTokens = promptTokens + completionTokens
		h.pool.IncrementUsage(key, promptTokens, completionTokens, totalTokens)
		go h.pool.LogRequest(&RequestLog{
			Method: "POST", Path: "/v1/messages", StatusCode: resp.StatusCode,
			LatencyMs: time.Since(start).Milliseconds(), ProxyKey: proxyKey, UpstreamKey: truncKey(key, 8),
			Model: model, PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens,
			RequestBody: string(body), ResponseBody: string(bodyBytes),
		})
		w.Write(bodyBytes)
	}
}

// MessagesCountTokens proxies POST /v1/messages/count_tokens to the upstream Anthropic-compatible API.
func (h *Handler) MessagesCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "Only POST method is allowed", "invalid_request_error")
		return
	}

	if !h.validateProxyAuth(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Invalid or missing API key"}}`))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error")
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		writeAnthropicError(w, http.StatusNotFound, "Channel not found", "not_found_error")
		return
	}

	key, _, err := h.pool.GetKey(channel.ID)
	if err != nil {
		writeAnthropicError(w, http.StatusServiceUnavailable, "No API key available", "api_error")
		return
	}

	upstreamURL := messagesCountTokensURLFromBase(channel.BaseURL)
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "Failed to create request", "api_error")
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	if av := r.Header.Get("anthropic-version"); av != "" {
		req.Header.Set("anthropic-version", av)
	} else {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	h.prepareUpstreamRequest(req)

	client, cerr := h.upstreamClientForChannel(channel, 30*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.recordKeyFailure(key)
	} else {
		h.resetKeyFailures(key)
	}

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	bodyBytes, _ := io.ReadAll(resp.Body)
	w.Write(bodyBytes)
}
