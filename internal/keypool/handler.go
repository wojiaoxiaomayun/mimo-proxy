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
		"stream":                false,
		"thinking":              map[string]string{"type": "disabled"},
	}
	b, _ := json.Marshal(msg)
	return b
}

// testMessagesPayload builds a minimal Anthropic messages request body for key verification.
func testMessagesPayload(model string) []byte {
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	msg := map[string]interface{}{
		"model":      model,
		"max_tokens": 16,
		"messages": []map[string]string{
			{"role": "user", "content": "Hi"},
		},
	}
	b, _ := json.Marshal(msg)
	return b
}

//go:embed ui/*
var uiEmbed embed.FS

type modelCache struct {
	models  []string
	updated time.Time
}

type Handler struct {
	pool       *KeyPool
	keyFails   map[string]int
	keyFailsMu sync.RWMutex
	modelCache map[int]*modelCache // channelID → cached models
	modelMu    sync.RWMutex
}

func NewMux(pool *KeyPool, defaultURL string) *http.ServeMux {
	h := &Handler{
		pool:       pool,
		keyFails:   make(map[string]int),
		modelCache: make(map[int]*modelCache),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", h.UIRedirect)
	mux.HandleFunc("/ui", h.UIRedirect)
	mux.HandleFunc("/ui/keys", h.UIKeys)
	mux.HandleFunc("/ui/channels", h.UIChannels)
	mux.HandleFunc("/ui/mappings", h.UIMappings)
	mux.HandleFunc("/ui/logs", h.UILogs)
	mux.HandleFunc("/ui/settings", h.UISettings)
	mux.HandleFunc("/ui/style.css", h.UIStaticCSS)
	mux.HandleFunc("/ui/common.js", h.UIStaticJS)
	// Type-prefixed routes
	mux.HandleFunc("/openai/v1/chat/completions", h.ChatCompletions)
	mux.HandleFunc("/openai/v1/models", h.V1ModelsOpenAI)
	mux.HandleFunc("/anthropic/v1/messages", h.Messages)
	mux.HandleFunc("/anthropic/v1/messages/count_tokens", h.MessagesCountTokens)
	mux.HandleFunc("/anthropic/v1/models", h.V1ModelsAnthropic)
	// Per-channel routes
	mux.HandleFunc("/c/{channel}/v1/chat/completions", h.ChatCompletions)
	mux.HandleFunc("/c/{channel}/v1/models", h.V1Models)
	mux.HandleFunc("/c/{channel}/v1/messages", h.Messages)
	mux.HandleFunc("/c/{channel}/v1/messages/count_tokens", h.MessagesCountTokens)
	mux.HandleFunc("/stats", h.Stats)
	mux.HandleFunc("/keys", h.Keys)
	mux.HandleFunc("/test-key", h.TestKey)
	mux.HandleFunc("/models", h.Models)
	mux.HandleFunc("/channels", h.Channels)
	mux.HandleFunc("/model-mappings", h.ModelMappings)
	mux.HandleFunc("/test-mapping", h.TestMapping)
	mux.HandleFunc("/settings", h.Settings)
	mux.HandleFunc("/health-check", h.HealthCheck)
	mux.HandleFunc("/proxy-keys", h.ProxyKeys)
	mux.HandleFunc("/backup", h.Backup)
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

func (h *Handler) UIRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/channels", http.StatusFound)
}

func (h *Handler) UIKeys(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/channels", http.StatusFound)
}

func (h *Handler) UIChannels(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/channels.html")
}

func (h *Handler) UIMappings(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/mappings.html")
}

func (h *Handler) UILogs(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/logs.html")
}

func (h *Handler) UISettings(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/settings.html")
}

func (h *Handler) UIStaticCSS(w http.ResponseWriter, r *http.Request) {
	data, err := uiEmbed.ReadFile("ui/style.css")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(data)
}

func (h *Handler) UIStaticJS(w http.ResponseWriter, r *http.Request) {
	data, err := uiEmbed.ReadFile("ui/common.js")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(data)
}

func (h *Handler) serveUIPage(w http.ResponseWriter, path string) {
	data, err := uiEmbed.ReadFile(path)
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

	// Check model mapping: if the request model is a mapping alias, swap model and channel.
	if mappedChannelID, mappedModel, ok := h.pool.ResolveModelMapping(extractModel(body)); ok && mappedModel != "" {
		if mc, merr := h.pool.GetChannelByID(mappedChannelID); merr == nil && mc.Enabled {
			body = replaceModel(body, mappedModel)
			channel = mc
		}
	}

	key, defaultModel, err := h.pool.GetKey(channel.ID)
	if err != nil {
		http.Error(w, "No API key available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Get or refresh model cache (1h TTL). Fetches from upstream on miss/expiry.
	models := h.getOrFetchModels(channel, key)
	body = h.swapModelIfNeeded(body, models, defaultModel)

	req, err := http.NewRequest(http.MethodPost, chatCompletionsURLFromBase(channel.BaseURL), bytes.NewReader(body))
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
	h.prepareUpstreamRequest(req)

	client, cerr := h.upstreamClientForChannel(channel, 120*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
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
		// In failover mode, disable the key and clear failover_key immediately.
		if channel.KeyMode == "failover" {
			h.pool.RotateFailoverKey(channel.ID, key)
		}
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
	totalTokens, totalCalls, todayTokens, todayCalls, enabled, disabled, _ := h.pool.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_calls":   totalCalls,
		"total_tokens":  totalTokens,
		"today_calls":   todayCalls,
		"today_tokens":  todayTokens,
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
		// Build channel lookup map for enriched response.
		channels, _ := h.pool.GetAllChannels()
		chMap := make(map[int]*ChannelInfo)
		for i := range channels {
			chMap[channels[i].ID] = &channels[i]
		}
		type KeyWithChannel struct {
			KeyInfo
			ChannelName string `json:"channel_name"`
			IsActive    bool   `json:"is_active"`
			IsPinned    bool   `json:"is_pinned"`
		}
		// Find the "next to use" key per channel (lowest usage_count when no pin).
		type chActive struct {
			minUsage, minID int
			key             string
		}
		chBest := make(map[int]*chActive)
		for _, k := range keys {
			if !k.Enabled {
				continue
			}
			b, ok := chBest[k.ChannelID]
			if !ok || k.UsageCount < b.minUsage || (k.UsageCount == b.minUsage && k.ID < b.minID) {
				chBest[k.ChannelID] = &chActive{minUsage: k.UsageCount, minID: k.ID, key: k.Key}
			}
		}
		var result []KeyWithChannel
		for _, k := range keys {
			kwc := KeyWithChannel{KeyInfo: k}
			if ch, ok := chMap[k.ChannelID]; ok {
				kwc.ChannelName = ch.Name
				if ch.PinnedKey != "" {
					kwc.IsActive = ch.PinnedKey == k.Key
					kwc.IsPinned = ch.PinnedKey == k.Key
				} else if b, ok := chBest[k.ChannelID]; ok {
					kwc.IsActive = b.key == k.Key
				}
			}
			result = append(result, kwc)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": result})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Action       string `json:"action"`
			Key          string `json:"key"`
			Note         string `json:"note"`
			ChannelID    int    `json:"channel_id"`
			DefaultModel string `json:"default_model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		var err error
		switch req.Action {
		case "add":
			err = h.pool.Add(req.Key, req.Note, req.ChannelID, req.DefaultModel)
		case "update-model":
			err = h.pool.UpdateKeyDefaultModel(req.Key, req.DefaultModel)
		case "update":
			err = h.pool.UpdateKey(req.Key, req.Note, req.DefaultModel)
		case "remove":
			err = h.pool.Remove(req.Key)
		case "enable":
			err = h.pool.Enable(req.Key)
		case "disable":
			err = h.pool.Disable(req.Key)
		case "update-note":
			err = h.pool.UpdateNote(req.Key, req.Note)
		case "pin-key":
			err = h.pool.PinKey(req.ChannelID, req.Key)
		case "unpin-key":
			err = h.pool.UnpinKey(req.ChannelID)
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
	testURL := chatCompletionsURLFromBase(channel.BaseURL)
	testBody := testPayload(req.Model)
	if channel.ChannelType == "anthropic" {
		testURL = messagesURLFromBase(channel.BaseURL)
		testBody = testMessagesPayload(req.Model)
	}
	proxyReq, err := http.NewRequest(http.MethodPost, testURL, bytes.NewReader(testBody))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	if channel.ChannelType == "anthropic" {
		proxyReq.Header.Set("x-api-key", req.Key)
		proxyReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		proxyReq.Header.Set("api-key", req.Key)
		proxyReq.Header.Set("Authorization", "Bearer "+req.Key)
	}
	h.prepareUpstreamRequest(proxyReq)

	client, cerr := h.upstreamClientForChannel(channel, 30*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
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

// modelsURLFromBase derives the models endpoint from the configured upstream URL.
func modelsURLFromBase(baseURL string) string {
	return upstreamEndpointURLFromBase(baseURL, "models")
}

// chatCompletionsURLFromBase derives the chat completions endpoint from the configured upstream URL.
func chatCompletionsURLFromBase(baseURL string) string {
	return upstreamEndpointURLFromBase(baseURL, "chat/completions")
}

func upstreamEndpointURLFromBase(baseURL string, endpoint string) string {
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
			return strings.TrimRight(u.String(), "/") + "/" + strings.TrimLeft(endpoint, "/")
		}
	}

	// No "v1" found in path — append /v1/{endpoint}.
	return strings.TrimRight(u.String(), "/") + "/v1/" + strings.TrimLeft(endpoint, "/")
}

// refreshModels fetches /v1/models from upstream and caches the result (1h TTL).
func (h *Handler) refreshModels(channelID int, modelsURL string, key string) []string {
	return h.refreshModelsWithType(channelID, modelsURL, key, "")
}

func (h *Handler) refreshModelsWithType(channelID int, modelsURL string, key string, channelType string) []string {
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil
	}
	// /v1/models is OpenAI-compatible for all channel types, always use Bearer auth.
	req.Header.Set("Authorization", "Bearer "+key)
	h.prepareUpstreamRequest(req)

	// Look up the channel so its proxy is honored.
	channel, _ := h.pool.GetChannelByID(channelID)
	client, _ := h.upstreamClientForChannel(channel, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &parsed)

	var models []string
	for _, m := range parsed.Data {
		models = append(models, m.ID)
	}

	h.modelMu.Lock()
	h.modelCache[channelID] = &modelCache{models: models, updated: time.Now()}
	h.modelMu.Unlock()

	return models
}

// getCachedModels returns cached models for a channel, or nil if not cached / expired (1h).
func (h *Handler) getCachedModels(channelID int) []string {
	h.modelMu.RLock()
	defer h.modelMu.RUnlock()
	entry, ok := h.modelCache[channelID]
	if !ok || time.Since(entry.updated) > time.Hour {
		return nil
	}
	return entry.models
}

// getOrFetchModels returns cached models or fetches from upstream if cache is empty/expired.
// If the channel has allowed_models configured, the returned list is filtered to only include those models.
func (h *Handler) getOrFetchModels(channel *ChannelInfo, key string) []string {
	models := h.getCachedModels(channel.ID)
	if models == nil {
		// Cache miss → fetch synchronously
		models = h.refreshModelsWithType(channel.ID, modelsURLFromBase(channel.BaseURL), key, channel.ChannelType)
	}
	if len(channel.AllowedModels) > 0 {
		allowed := make(map[string]bool)
		for _, m := range channel.AllowedModels {
			allowed[m] = true
		}
		var filtered []string
		for _, m := range models {
			if allowed[m] {
				filtered = append(filtered, m)
			}
		}
		return filtered
	}
	return models
}

// swapModelIfNeeded replaces the model if it's not in the cached list.
func (h *Handler) swapModelIfNeeded(body []byte, models []string, defaultModel string) []byte {
	if len(models) == 0 || defaultModel == "" {
		return body // no cache or no default → pass through
	}
	var parsed map[string]interface{}
	if json.Unmarshal(body, &parsed) != nil {
		return body
	}
	reqModel, _ := parsed["model"].(string)
	if reqModel == "" {
		return body
	}
	for _, m := range models {
		if m == reqModel {
			return body
		}
	}
	parsed["model"] = defaultModel
	newBody, _ := json.Marshal(parsed)
	return newBody
}

// validateProxyAuth checks the client's Authorization header (or x-api-key) against proxy_keys.
// Supports both Bearer token and Anthropic-style x-api-key header.
func (h *Handler) validateProxyAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	token := ""
	if auth != "" {
		token = auth
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = auth[7:]
		}
	} else if xKey := r.Header.Get("x-api-key"); xKey != "" {
		token = xKey
	}
	if token == "" {
		return false
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

// extractProxyKey extracts the proxy key from the Authorization header or x-api-key header.
func extractProxyKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			return auth[7:]
		}
		return auth
	}
	return r.Header.Get("x-api-key")
}

// extractModel extracts the model field from a JSON request body.
func extractModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &m)
	return m.Model
}

// replaceModel replaces the model field in a JSON request body.
func replaceModel(body []byte, newModel string) []byte {
	var parsed map[string]interface{}
	if json.Unmarshal(body, &parsed) != nil {
		return body
	}
	parsed["model"] = newModel
	newBody, _ := json.Marshal(parsed)
	return newBody
}

func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET method is allowed", http.StatusMethodNotAllowed)
		return
	}

	channelID := 0
	fmt.Sscanf(r.URL.Query().Get("channel"), "%d", &channelID)
	key := r.URL.Query().Get("key")
	baseURL := strings.TrimSpace(r.URL.Query().Get("base_url"))

	if key == "" {
		http.Error(w, `{"error":"Missing key parameter"}`, http.StatusBadRequest)
		return
	}

	var channel *ChannelInfo
	if baseURL == "" {
		// Resolve channel
		if channelID > 0 {
			channels, _ := h.pool.GetAllChannels()
			for _, c := range channels {
				if c.ID == channelID {
					channel = &c
					break
				}
			}
		}
		if channel == nil {
			channel, _ = h.pool.GetDefaultChannel()
		}
		if channel == nil {
			http.Error(w, `{"error":"No channel found"}`, http.StatusNotFound)
			return
		}
		baseURL = channel.BaseURL
	}

	modelsURL := modelsURLFromBase(baseURL)

	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	// /v1/models is OpenAI-compatible for all channel types, always use Bearer auth.
	req.Header.Set("Authorization", "Bearer "+key)
	h.prepareUpstreamRequest(req)

	client, _ := h.upstreamClientForChannel(channel, 15*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to fetch models: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Cache models for persisted channels (populated when user clicks refresh in UI).
	if channel != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			var ids []string
			for _, m := range parsed.Data {
				ids = append(ids, m.ID)
			}
			h.modelMu.Lock()
			h.modelCache[channel.ID] = &modelCache{models: ids, updated: time.Now()}
			h.modelMu.Unlock()
		}
	}

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

	key, _, err := h.pool.GetKey(channel.ID)
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
	// /v1/models is OpenAI-compatible for all channel types, always use Bearer auth.
	req.Header.Set("Authorization", "Bearer "+key)
	h.prepareUpstreamRequest(req)

	client, _ := h.upstreamClientForChannel(channel, 15*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":{"message":"Upstream request failed: `+err.Error()+`","code":"502"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Filter models by channel allowed_models if configured.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && len(channel.AllowedModels) > 0 {
		var parsed struct {
			Object string                   `json:"object"`
			Data   []map[string]interface{} `json:"data"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			allowed := make(map[string]bool)
			for _, m := range channel.AllowedModels {
				allowed[m] = true
			}
			var filtered []map[string]interface{}
			for _, m := range parsed.Data {
				if id, ok := m["id"].(string); ok && allowed[id] {
					filtered = append(filtered, m)
				}
			}
			parsed.Data = filtered
			if b, err := json.Marshal(parsed); err == nil {
				body = b
			}
		}
	}

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

// V1ModelsOpenAI returns models from all OpenAI-type channels + mapped models.
func (h *Handler) V1ModelsOpenAI(w http.ResponseWriter, r *http.Request) {
	h.v1ModelsByType(w, r, "openai")
}

// V1ModelsAnthropic returns models from all Anthropic-type channels + mapped models.
func (h *Handler) V1ModelsAnthropic(w http.ResponseWriter, r *http.Request) {
	h.v1ModelsByType(w, r, "anthropic")
}

func (h *Handler) v1ModelsByType(w http.ResponseWriter, r *http.Request, channelType string) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":{"message":"Method not allowed","code":"405"}}`, http.StatusMethodNotAllowed)
		return
	}
	if !h.validateProxyAuth(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid or missing API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
		return
	}

	// Build reverse mapping: target_model → mapping_name (for this channel type)
	mappedNames := make(map[string]string) // target_model → mapping alias
	mappedOnly := make(map[string]bool)    // all mapping names for this type
	if mappings, merr := h.pool.GetAllModelMappings(); merr == nil {
		for _, m := range mappings {
			if !m.Enabled {
				continue
			}
			// Check if any target belongs to this channel type
			hasType := false
			for _, t := range m.Targets {
				if t.Enabled && t.ChannelType == channelType {
					hasType = true
					mappedNames[t.TargetModel] = m.Name
				}
			}
			if hasType {
				mappedOnly[m.Name] = true
			}
		}
	}

	// If no mappings exist for this type, return empty list.
	if len(mappedOnly) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []interface{}{}})
		return
	}

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	seen := make(map[string]bool)
	var models []modelEntry

	channels, _ := h.pool.GetAllChannels()
	for _, ch := range channels {
		if !ch.Enabled || ch.ChannelType != channelType {
			continue
		}
		key, _, err := h.pool.GetKey(ch.ID)
		if err != nil {
			continue
		}
		modelsURL := modelsURLFromBase(ch.BaseURL)
		req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+key)
		h.prepareUpstreamRequest(req)

		client, _ := h.upstreamClientForChannel(&ch, 10*time.Second)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}

		var parsed struct {
			Data []struct {
				ID      string `json:"id"`
				OwnedBy string `json:"owned_by"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &parsed) != nil {
			continue
		}

		// Apply allowed_models filter
		allowedSet := make(map[string]bool)
		if len(ch.AllowedModels) > 0 {
			for _, m := range ch.AllowedModels {
				allowedSet[m] = true
			}
		}
		for _, m := range parsed.Data {
			if len(allowedSet) > 0 && !allowedSet[m.ID] {
				continue
			}
			// Only include models that have a mapping.
			alias, hasMapping := mappedNames[m.ID]
			if !hasMapping {
				continue // skip unmapped upstream models
			}
			delete(mappedOnly, alias) // mark as backed by upstream
			if !seen[alias] {
				seen[alias] = true
				models = append(models, modelEntry{ID: alias, Object: "model", OwnedBy: m.OwnedBy})
			}
		}
	}

	// Add remaining mappings not backed by any upstream model (standalone aliases).
	for name := range mappedOnly {
		if !seen[name] {
			seen[name] = true
			models = append(models, modelEntry{ID: name, Object: "model", OwnedBy: "mapping"})
		}
	}

	result := map[string]interface{}{
		"object": "list",
		"data":   models,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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

func (h *Handler) ModelMappings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		mappings, err := h.pool.GetAllModelMappings()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"mappings": mappings})

	case http.MethodPost:
		var req struct {
			Action      string               `json:"action"`
			ID          int                  `json:"id"`
			Name        string               `json:"name"`
			ChannelType string               `json:"channel_type"`
			Strategy    string               `json:"strategy"`
			Note        string               `json:"note"`
			Targets     []ModelMappingTarget `json:"targets"`
			// Legacy fields (for backward compat)
			ChannelID   int    `json:"channel_id"`
			TargetModel string `json:"target_model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"Invalid request"}`, http.StatusBadRequest)
			return
		}

		var err error
		switch req.Action {
		case "add":
			targets := req.Targets
			if len(targets) == 0 && req.ChannelID > 0 {
				targets = []ModelMappingTarget{{ChannelID: req.ChannelID, TargetModel: req.TargetModel}}
			}
			err = h.pool.AddModelMappingGroup(req.Name, req.ChannelType, req.Strategy, req.Note, targets)
		case "update":
			targets := req.Targets
			if len(targets) == 0 && req.ChannelID > 0 {
				targets = []ModelMappingTarget{{ChannelID: req.ChannelID, TargetModel: req.TargetModel}}
			}
			err = h.pool.UpdateModelMappingGroup(req.ID, req.Name, req.ChannelType, req.Strategy, req.Note, targets)
		case "remove":
			err = h.pool.RemoveModelMapping(req.ID)
		case "get":
			m, gerr := h.pool.GetModelMappingByID(req.ID)
			if gerr != nil {
				http.Error(w, `{"error":"`+gerr.Error()+`"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(m)
			return
		default:
			http.Error(w, `{"error":"Unknown action"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// TestMapping sends a real request to the upstream API using the mapping's channel and target model,
// verifying the mapping is reachable and the target model exists.
func (h *Handler) TestMapping(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID          int    `json:"id"`
		ChannelID   int    `json:"channel_id"`
		TargetModel string `json:"target_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Invalid request"}`, http.StatusBadRequest)
		return
	}

	// If ID is provided, look up the mapping and use first target
	if req.ID > 0 {
		mappings, _ := h.pool.GetAllModelMappings()
		for _, m := range mappings {
			if m.ID == req.ID {
				if len(m.Targets) > 0 {
					req.ChannelID = m.Targets[0].ChannelID
					req.TargetModel = m.Targets[0].TargetModel
				} else {
					req.ChannelID = m.ChannelID
					req.TargetModel = m.TargetModel
				}
				break
			}
		}
	}
	if req.ChannelID == 0 {
		http.Error(w, `{"error":"Channel not found"}`, http.StatusBadRequest)
		return
	}

	// Resolve channel
	var channel *ChannelInfo
	channels, _ := h.pool.GetAllChannels()
	for _, c := range channels {
		if c.ID == req.ChannelID {
			channel = &c
			break
		}
	}
	if channel == nil {
		http.Error(w, `{"error":"Channel not found"}`, http.StatusNotFound)
		return
	}

	// Get a key for this channel
	key, _, err := h.pool.GetKey(channel.ID)
	if err != nil || key == "" {
		http.Error(w, `{"error":"No available key for channel"}`, http.StatusBadGateway)
		return
	}

	// Build and send test request
	start := time.Now()
	testURL := chatCompletionsURLFromBase(channel.BaseURL)
	testBody := testPayload(req.TargetModel)
	if channel.ChannelType == "anthropic" {
		testURL = messagesURLFromBase(channel.BaseURL)
		testBody = testMessagesPayload(req.TargetModel)
	}

	proxyReq, err := http.NewRequest(http.MethodPost, testURL, bytes.NewReader(testBody))
	if err != nil {
		http.Error(w, `{"error":"Failed to create request"}`, http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	if channel.ChannelType == "anthropic" {
		proxyReq.Header.Set("x-api-key", key)
		proxyReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		proxyReq.Header.Set("api-key", key)
		proxyReq.Header.Set("Authorization", "Bearer "+key)
	}
	h.prepareUpstreamRequest(proxyReq)

	client, cerr := h.upstreamClientForChannel(channel, 30*time.Second)
	if cerr != nil {
		log.Printf("[proxy] fallback to direct: %v", cerr)
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, `{"error":"Request failed: `+err.Error()+`"}`, http.StatusBadGateway)
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
			Action        string   `json:"action"`
			ID            int      `json:"id"`
			Name          string   `json:"name"`
			Prefix        string   `json:"prefix"`
			BaseURL       string   `json:"base_url"`
			WebsiteURL    string   `json:"website_url"`
			ChannelType   string   `json:"channel_type"`
			ProxyURL      string   `json:"proxy_url"`
			KeyMode       string   `json:"key_mode"`
			AllowedModels []string `json:"allowed_models"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"Invalid request"}`, http.StatusBadRequest)
			return
		}

		switch req.Action {
		case "add":
			id, err := h.pool.AddChannel(req.Name, req.Prefix, req.BaseURL, req.WebsiteURL, req.ChannelType, req.ProxyURL, req.AllowedModels)
			if err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			if req.KeyMode != "" && req.KeyMode != "round-robin" {
				h.pool.SetKeyMode(id, req.KeyMode)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "id": id})
		case "update":
			if err := h.pool.UpdateChannel(req.ID, req.Name, req.Prefix, req.BaseURL, req.WebsiteURL, req.ChannelType, req.ProxyURL, req.AllowedModels); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
				return
			}
			// Also update key_mode if provided.
			if req.KeyMode != "" {
				h.pool.SetKeyMode(req.ID, req.KeyMode)
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "set-key-mode":
			if err := h.pool.SetKeyMode(req.ID, req.KeyMode); err != nil {
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

func (h *Handler) Backup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		backup, err := h.pool.ExportBackup()
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		body, err := json.MarshalIndent(backup, "", "  ")
		if err != nil {
			http.Error(w, `{"error":"Failed to encode backup"}`, http.StatusInternalServerError)
			return
		}
		filename := "mimo-proxy-backup-" + time.Now().Format("20060102-150405") + ".json"
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Write(body)

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		var backup BackupData
		if err := json.NewDecoder(r.Body).Decode(&backup); err != nil {
			http.Error(w, `{"error":"Invalid backup file"}`, http.StatusBadRequest)
			return
		}
		summary, err := h.pool.ImportBackup(&backup)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		h.modelMu.Lock()
		h.modelCache = make(map[int]*modelCache)
		h.modelMu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "summary": summary})

	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
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
			h.prepareUpstreamRequest(testReq)

			client, _ := h.upstreamClientForChannel(ch, 15*time.Second)
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
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Only POST method is allowed"}}`, http.StatusMethodNotAllowed)
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
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Failed to read request body"}}`, http.StatusBadRequest)
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"not_found_error","message":"Channel not found"}}`, http.StatusNotFound)
		return
	}

	// Check model mapping: if the request model is a mapping alias, swap model and channel.
	if mappedChannelID, mappedModel, ok := h.pool.ResolveModelMapping(extractModel(body)); ok && mappedModel != "" {
		if mc, merr := h.pool.GetChannelByID(mappedChannelID); merr == nil && mc.Enabled {
			body = replaceModel(body, mappedModel)
			channel = mc
		}
	}

	key, defaultModel, err := h.pool.GetKey(channel.ID)
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"No API key available: `+err.Error()+`"}}`, http.StatusServiceUnavailable)
		return
	}

	// Swap model if needed (reuse same logic).
	var models []string
	models = h.getCachedModels(channel.ID)
	if models == nil {
		models = h.refreshModels(channel.ID, modelsURLFromBase(channel.BaseURL), key)
	}
	body = h.swapModelIfNeeded(body, models, defaultModel)

	upstreamURL := messagesURLFromBase(channel.BaseURL)
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"Failed to create request"}}`, http.StatusInternalServerError)
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
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"Failed to proxy request: `+err.Error()+`"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Track key health
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		h.recordKeyFailure(key)
		if channel.KeyMode == "failover" {
			h.pool.RotateFailoverKey(channel.ID, key)
		}
	} else {
		h.resetKeyFailures(key)
	}

	// Forward response headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	proxyKey := extractProxyKey(r)
	model := extractModel(body)

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
			// Extract usage from Anthropic streaming: message_start or message_delta events.
			if bytes.HasPrefix(line, []byte("event: message_start")) {
				// Next line should be data with usage
			}
			if bytes.HasPrefix(line, []byte("data: ")) && !bytes.Contains(line, []byte("[DONE]")) {
				var chunk map[string]interface{}
				if json.Unmarshal(line[6:], &chunk) == nil {
					// Anthropic: usage in message_start
					if usage, ok := chunk["usage"].(map[string]interface{}); ok {
						if v, ok := usage["input_tokens"].(float64); ok {
							promptTokens = int(v)
						}
						if v, ok := usage["output_tokens"].(float64); ok {
							completionTokens = int(v)
						}
					}
					// Anthropic: usage in message_delta
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
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Only POST method is allowed"}}`, http.StatusMethodNotAllowed)
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
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Failed to read request body"}}`, http.StatusBadRequest)
		return
	}

	channel, err := h.resolveChannel(r)
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"not_found_error","message":"Channel not found"}}`, http.StatusNotFound)
		return
	}

	key, _, err := h.pool.GetKey(channel.ID)
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"No API key available"}}`, http.StatusServiceUnavailable)
		return
	}

	upstreamURL := messagesCountTokensURLFromBase(channel.BaseURL)
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"Failed to create request"}}`, http.StatusInternalServerError)
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
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"Upstream request failed: `+err.Error()+`"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
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
