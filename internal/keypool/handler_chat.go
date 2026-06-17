package keypool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Only POST method is allowed", "invalid_request_error", "405")
		return
	}

	// Validate proxy key
	if !h.validateProxyAuth(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid or missing API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
		return
	}

	start := time.Now()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Failed to read request body", "invalid_request_error", "400")
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "Channel not found", "invalid_request_error", "404")
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
		writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Model mapping '%s' is disabled", originalModel), "invalid_request_error", "mapping_disabled")
		return
	}
	log.Printf("[chat] originalModel=%s mappedModel=%s channel=%s(%d) modelMapped=%v", originalModel, mappedModelName, channel.Name, channel.ID, modelMapped)

	key, defaultModel, err := h.pool.GetKey(channel.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "No API key available: "+err.Error(), "api_error", "503")
		return
	}

	// Get or refresh model cache (1h TTL). Fetches from upstream on miss/expiry.
	// Skip swapModelIfNeeded when a model mapping was applied to preserve the mapped target model.
	if !modelMapped {
		models := h.getOrFetchModels(channel, key)
		body = h.swapModelIfNeeded(body, models, defaultModel)
	}

	req, err := http.NewRequest(http.MethodPost, chatCompletionsURLFromBase(channel.BaseURL), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "Failed to create request", "api_error", "500")
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", key)

	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	h.prepareUpstreamRequest(req)

	client, cerr := h.upstreamClientForChannel(channel, 120*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
	resp, err := client.Do(req)
	if err != nil {
		h.recordKeyFailure(key)
		writeOpenAIError(w, http.StatusBadGateway, "Failed to proxy request: "+err.Error(), "api_error", "502")
		return
	}
	defer resp.Body.Close()

	// Forward response headers.
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	proxyKey := extractProxyKey(r)
	// Use the original model name (before mapping) for logging.
	model := originalModel

	// keyOK tracks whether the key should be considered healthy.
	// Starts as true when status is 200; set to false if body contains an error.
	keyOK := resp.StatusCode == http.StatusOK
	// isBadKey is true when status is 401/403 (permanently invalid key).
	isBadKey := resp.StatusCode == 401 || resp.StatusCode == 403

	// Check if this is a streaming response.
	isStreaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	if isStreaming {
		// Stream: forward chunks line by line, accumulate for logging.
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
			// Accumulate all lines for logging.
			streamBuf.Write(line)
			streamBuf.WriteByte('\n')
			// Check for SSE error event (upstream returned 200 but body has error).
			if bytes.HasPrefix(line, []byte("data: ")) && !bytes.Contains(line, []byte("[DONE]")) {
				var chunk map[string]interface{}
				if json.Unmarshal(line[6:], &chunk) == nil {
					if _, hasErr := chunk["error"]; hasErr {
						keyOK = false
					}
					if usage, ok := chunk["usage"].(map[string]interface{}); ok {
						if v, ok := usage["prompt_tokens"].(float64); ok {
							promptTokens = int(v)
						}
						if v, ok := usage["completion_tokens"].(float64); ok {
							completionTokens = int(v)
						}
						if v, ok := usage["total_tokens"].(float64); ok {
							totalTokens = int(v)
						}
					}
				}
			}
		}
		// Track key health after reading the full response.
		if !keyOK {
			h.recordKeyFailure(key)
			if isBadKey && channel.KeyMode == "failover" {
				h.pool.RotateFailoverKey(channel.ID, key)
			}
		} else {
			h.resetKeyFailures(key)
		}
		h.pool.IncrementUsage(key, promptTokens, completionTokens, totalTokens)
		go h.pool.LogRequest(&RequestLog{
			Method: "POST", Path: "/v1/chat/completions", StatusCode: resp.StatusCode,
			LatencyMs: time.Since(start).Milliseconds(), ProxyKey: proxyKey, UpstreamKey: truncKey(key, 8),
			Model: model, PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens,
			RequestBody: string(body), ResponseBody: streamBuf.String(),
		})
	} else {
		// Non-streaming: read full body, extract usage, write to client.
		bodyBytes, _ := io.ReadAll(resp.Body)
		var promptTokens, completionTokens, totalTokens int
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var parsed map[string]interface{}
			if json.Unmarshal(bodyBytes, &parsed) == nil {
				// Check for error field in response body (upstream returned 200 but has error).
				if _, hasErr := parsed["error"]; hasErr {
					keyOK = false
				}
				if usage, ok := parsed["usage"].(map[string]interface{}); ok {
					if v, ok := usage["prompt_tokens"].(float64); ok {
						promptTokens = int(v)
					}
					if v, ok := usage["completion_tokens"].(float64); ok {
						completionTokens = int(v)
					}
					if v, ok := usage["total_tokens"].(float64); ok {
						totalTokens = int(v)
					}
				}
			}
		}
		// Track key health after reading the full response.
		if !keyOK {
			h.recordKeyFailure(key)
			if isBadKey && channel.KeyMode == "failover" {
				h.pool.RotateFailoverKey(channel.ID, key)
			}
		} else {
			h.resetKeyFailures(key)
		}
		h.pool.IncrementUsage(key, promptTokens, completionTokens, totalTokens)
		go h.pool.LogRequest(&RequestLog{
			Method: "POST", Path: "/v1/chat/completions", StatusCode: resp.StatusCode,
			LatencyMs: time.Since(start).Milliseconds(), ProxyKey: proxyKey, UpstreamKey: truncKey(key, 8),
			Model: model, PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens,
			RequestBody: string(body), ResponseBody: string(bodyBytes),
		})
		w.Write(bodyBytes)
	}
}
