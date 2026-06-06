package keypool

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// testPayload builds a minimal chat completion request body for key verification.
func testPayload(model string) []byte {
	if model == "" {
		model = "mimo-v2.5-pro"
	}
	// Use json.Marshal to safely escape the model name.
	msg := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Hi"},
		},
		"max_completion_tokens": 16,
		"stream":               false,
		"thinking":             map[string]string{"type": "disabled"},
	}
	b, _ := json.Marshal(msg)
	return b
}

//go:embed index.html
var htmlEmbed embed.FS

type Handler struct {
	pool       *KeyPool
	keyFails   map[string]int // consecutive failures per key
	keyFailsMu sync.RWMutex
}

func NewMux(pool *KeyPool, defaultURL string) *http.ServeMux {
	h := &Handler{
		pool:     pool,
		keyFails: make(map[string]int),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", h.Index)
	mux.HandleFunc("/ui", h.Index)
	mux.HandleFunc("/v1/chat/completions", h.ChatCompletions)
	mux.HandleFunc("/v1/models", h.V1Models)
	mux.HandleFunc("/c/{channel}/v1/chat/completions", h.ChatCompletions)
	mux.HandleFunc("/c/{channel}/v1/models", h.V1Models)
	mux.HandleFunc("/stats", h.Stats)
	mux.HandleFunc("/keys", h.Keys)
	mux.HandleFunc("/test-key", h.TestKey)
	mux.HandleFunc("/models", h.Models)
	mux.HandleFunc("/channels", h.Channels)
	mux.HandleFunc("/settings", h.Settings)
	mux.HandleFunc("/health-check", h.HealthCheck)
	mux.HandleFunc("/proxy-keys", h.ProxyKeys)
	mux.HandleFunc("/logs", h.Logs)

	// Periodic log cleanup (every 5 minutes)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			h.pool.CleanLogs()
		}
	}()

	return mux
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	data, err := htmlEmbed.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Failed to load page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
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
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		http.Error(w, `{"error":{"message":"Channel not found","code":"404"}}`, http.StatusNotFound)
		return
	}

	key, err := h.pool.GetKey(channel.ID)
	if err != nil {
		http.Error(w, "No API key available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	req, err := http.NewRequest(http.MethodPost, channel.BaseURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
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

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		h.recordKeyFailure(key)
		http.Error(w, "Failed to proxy request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Track key health: 401/403 indicate a bad key.
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		h.recordKeyFailure(key)
	} else {
		h.resetKeyFailures(key)
	}

	// Forward response headers.
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	proxyKey := extractProxyKey(r)
	model := extractModel(body)

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
			// Try to extract usage from data: chunks.
			if bytes.HasPrefix(line, []byte("data: ")) && !bytes.Contains(line, []byte("[DONE]")) {
				var chunk map[string]interface{}
				if json.Unmarshal(line[6:], &chunk) == nil {
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

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	totalTokens, totalCalls, enabled, disabled, _ := h.pool.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_calls":   totalCalls,
		"total_tokens":  totalTokens,
		"enabled_keys":  enabled,
		"disabled_keys": disabled,
	})
}

func (h *Handler) Keys(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		channelID := 0
		fmt.Sscanf(r.URL.Query().Get("channel"), "%d", &channelID)
		keys, err := h.pool.GetAll(channelID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Action    string `json:"action"`
			Key       string `json:"key"`
			Note      string `json:"note"`
			ChannelID int    `json:"channel_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		var err error
		switch req.Action {
		case "add":
			err = h.pool.Add(req.Key, req.Note, req.ChannelID)
		case "remove":
			err = h.pool.Remove(req.Key)
		case "enable":
			err = h.pool.Enable(req.Key)
		case "disable":
			err = h.pool.Disable(req.Key)
		case "update-note":
			err = h.pool.UpdateNote(req.Key, req.Note)
		default:
			http.Error(w, fmt.Sprintf("Unknown action: %s", req.Action), http.StatusBadRequest)
			return
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (h *Handler) TestKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Key       string `json:"key"`
		Model     string `json:"model"`
		ChannelID int    `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		http.Error(w, "Missing or invalid key", http.StatusBadRequest)
		return
	}

	// Resolve channel
	var channel *ChannelInfo
	if req.ChannelID > 0 {
		channels, _ := h.pool.GetAllChannels()
		for _, c := range channels {
			if c.ID == req.ChannelID {
				channel = &c
				break
			}
		}
	}
	if channel == nil {
		channel, _ = h.pool.GetDefaultChannel()
	}
	if channel == nil {
		http.Error(w, `{"error":"No channel configured"}`, http.StatusInternalServerError)
		return
	}

	start := time.Now()
	proxyReq, err := http.NewRequest(http.MethodPost, channel.BaseURL, bytes.NewReader(testPayload(req.Model)))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("api-key", req.Key)
	proxyReq.Header.Set("Authorization", "Bearer "+req.Key)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	latency := time.Since(start).Milliseconds()

	body, _ := io.ReadAll(resp.Body)

	w.Header().Set("Content-Type", "application/json")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"latency": latency,
			"data":    result,
		})
	} else {
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     "error",
			"code":       resp.StatusCode,
			"latency":    latency,
			"error_body": result,
		})
	}
}

// recordKeyFailure increments the consecutive failure count for a key.
// After 3 consecutive failures the key is automatically disabled.
func (h *Handler) recordKeyFailure(key string) {
	h.keyFailsMu.Lock()
	h.keyFails[key]++
	count := h.keyFails[key]
	h.keyFailsMu.Unlock()

	if count >= 3 {
		log.Printf("[auto-disable] key %s...%s failed %d times consecutively, disabling",
			truncKey(key, 4), truncKey(key, -4), count)
		h.pool.Disable(key)
	}
}

// resetKeyFailures clears the consecutive failure count for a key.
func (h *Handler) resetKeyFailures(key string) {
	h.keyFailsMu.Lock()
	delete(h.keyFails, key)
	h.keyFailsMu.Unlock()
}

// resolveChannel extracts channel from URL path and returns it.
// /c/{channel}/v1/... → uses specified channel
// /v1/... → uses default channel
func (h *Handler) resolveChannel(r *http.Request) (*ChannelInfo, error) {
	if ch := r.PathValue("channel"); ch != "" {
		return h.pool.GetChannelByPrefix(ch)
	}
	return h.pool.GetDefaultChannel()
}

// modelsURLFromBase derives /v1/models from a chat completions base URL.
func modelsURLFromBase(baseURL string) string {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/v1/models"
}

// validateProxyAuth checks the client's Authorization header against proxy_keys.
func (h *Handler) validateProxyAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}
	token := auth
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token = auth[7:]
	}
	return h.pool.ValidateProxyKey(strings.TrimSpace(token))
}

// truncKey returns the first or last n characters of a key for logging.
func truncKey(key string, n int) string {
	if n > 0 && len(key) > n {
		return key[:n]
	}
	if n < 0 && len(key) > -n {
		return key[len(key)+n:]
	}
	return key
}

// extractProxyKey extracts the proxy key from the Authorization header.
func extractProxyKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return auth[7:]
	}
	return auth
}

// extractModel extracts the model field from a JSON request body.
func extractModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &m)
	return m.Model
}

func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET method is allowed", http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	channelID := 0
	fmt.Sscanf(r.URL.Query().Get("channel"), "%d", &channelID)

	if key == "" {
		http.Error(w, "Missing key query parameter", http.StatusBadRequest)
		return
	}

	var modelsURL string
	if channelID > 0 {
		// Use specific channel
		channels, _ := h.pool.GetAllChannels()
		for _, c := range channels {
			if c.ID == channelID {
				modelsURL = modelsURLFromBase(c.BaseURL)
				break
			}
		}
	}
	if modelsURL == "" {
		// Default channel
		ch, _ := h.pool.GetDefaultChannel()
		if ch != nil {
			modelsURL = modelsURLFromBase(ch.BaseURL)
		}
	}

	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("api-key", key)
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch models: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// V1Models proxies GET /v1/models to the upstream API using a key from the pool.
// This is the OpenAI-compatible endpoint that SDKs call via client.models.list().
func (h *Handler) V1Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":{"message":"Method not allowed","code":"405"}}`, http.StatusMethodNotAllowed)
		return
	}

	// Validate proxy key
	if !h.validateProxyAuth(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid or missing API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		http.Error(w, `{"error":{"message":"Channel not found","code":"404"}}`, http.StatusNotFound)
		return
	}

	key, err := h.pool.GetKey(channel.ID)
	if err != nil {
		http.Error(w, `{"error":{"message":"No API key available: `+err.Error()+`","code":"503"}}`, http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	// Derive models URL from channel base URL
	modelsURL := modelsURLFromBase(channel.BaseURL)

	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to create request","code":"500"}}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("api-key", key)
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":{"message":"Upstream request failed: `+err.Error()+`","code":"502"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Log request
	proxyKey := extractProxyKey(r)
	latency := time.Since(start).Milliseconds()
	go h.pool.LogRequest(&RequestLog{
		Method: "GET", Path: "/v1/models", StatusCode: resp.StatusCode,
		LatencyMs: latency, ProxyKey: proxyKey, UpstreamKey: truncKey(key, 8),
		ResponseBody: string(body),
	})

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (h *Handler) ProxyKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		keys, err := h.pool.GetAllProxyKeys()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})

	case http.MethodPost:
		var req struct {
			Action string `json:"action"`
			Key    string `json:"key"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"Invalid request"}`, http.StatusBadRequest)
			return
		}

		switch req.Action {
		case "generate":
			key, err := h.pool.GenerateProxyKey(req.Note)
			if err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "key": key})

		case "remove":
			if err := h.pool.RemoveProxyKey(req.Key); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		case "enable":
			if err := h.pool.EnableProxyKey(req.Key); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		case "disable":
			if err := h.pool.DisableProxyKey(req.Key); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		default:
			http.Error(w, `{"error":"Unknown action"}`, http.StatusBadRequest)
		}

	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) Logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET method is allowed", http.StatusMethodNotAllowed)
		return
	}

	// Detail: GET /logs?id=123
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		var id int
		fmt.Sscanf(idStr, "%d", &id)
		log, err := h.pool.GetLogDetail(id)
		if err != nil {
			http.Error(w, `{"error":"Log not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(log)
		return
	}

	page := 1
	fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
	if page < 1 {
		page = 1
	}
	logs, total, err := h.pool.GetLogs(page, 10)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"logs":  logs,
		"total": total,
		"page":  page,
	})
}

func (h *Handler) Channels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		channels, err := h.pool.GetAllChannels()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"channels": channels})

	case http.MethodPost:
		var req struct {
			Action string `json:"action"`
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			BaseURL string `json:"base_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"Invalid request"}`, http.StatusBadRequest)
			return
		}

		switch req.Action {
		case "add":
			if err := h.pool.AddChannel(req.Name, req.Prefix, req.BaseURL); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "update":
			if err := h.pool.UpdateChannel(req.ID, req.Name, req.Prefix, req.BaseURL); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "remove":
			h.pool.RemoveChannel(req.ID)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "enable":
			h.pool.EnableChannel(req.ID)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "disable":
			h.pool.DisableChannel(req.ID)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "set-default":
			h.pool.SetDefaultChannel(req.ID)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, `{"error":"Unknown action"}`, http.StatusBadRequest)
		}

	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Threshold int `json:"threshold"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Threshold <= 0 {
		req.Threshold = 3
	}

	keys, err := h.pool.GetAll(0)
	if err != nil {
		http.Error(w, `{"error":"Failed to get keys"}`, http.StatusInternalServerError)
		return
	}

	enabled := make([]KeyInfo, 0)
	for _, k := range keys {
		if k.Enabled {
			enabled = append(enabled, k)
		}
	}

	if len(enabled) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results":        []interface{}{},
			"disabled_count": 0,
		})
		return
	}

	type checkResult struct {
		Key      string `json:"key"`
		Status   string `json:"status"`
		Latency  int64  `json:"latency"`
		Error    string `json:"error,omitempty"`
		Disabled bool   `json:"disabled"`
	}

	results := make([]checkResult, 0, len(enabled))
	disabledCount := 0

	for _, k := range enabled {
		fails := 0
		var lastLatency int64
		var lastErr string

		for attempt := 0; attempt < req.Threshold; attempt++ {
			start := time.Now()
			ch, _ := h.pool.GetDefaultChannel()
			modelsURL := ""
			if ch != nil {
				modelsURL = modelsURLFromBase(ch.BaseURL)
			}
			testReq, err := http.NewRequest(http.MethodGet, modelsURL, nil)
			if err != nil {
				fails++
				lastErr = "request build failed"
				continue
			}
			testReq.Header.Set("api-key", k.Key)
			testReq.Header.Set("Authorization", "Bearer "+k.Key)

			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(testReq)
			if err != nil {
				fails++
				lastLatency = time.Since(start).Milliseconds()
				lastErr = err.Error()
				continue
			}
			resp.Body.Close()
			lastLatency = time.Since(start).Milliseconds()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				fails = 0
				lastErr = ""
				break
			}
			fails++
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}

		masked := k.Key
		if len(masked) > 8 {
			masked = masked[:4] + "••••" + masked[len(masked)-4:]
		} else if len(masked) > 4 {
			masked = "••••" + masked[len(masked)-4:]
		}

		if fails >= req.Threshold {
			h.pool.Disable(k.Key)
			disabledCount++
			results = append(results, checkResult{
				Key:      masked,
				Status:   "fail",
				Latency:  lastLatency,
				Error:    lastErr,
				Disabled: true,
			})
		} else {
			results = append(results, checkResult{
				Key:     masked,
				Status:  "ok",
				Latency: lastLatency,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results":        results,
		"disabled_count": disabledCount,
	})
}