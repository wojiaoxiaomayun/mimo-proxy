package keypool

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var htmlEmbed embed.FS

type Handler struct {
	pool      *KeyPool
	targetURL string
	stats     Stats
}

type Stats struct {
	TotalRequests int64
	mu            sync.RWMutex
}

func NewMux(pool *KeyPool, targetURL string) *http.ServeMux {
	h := &Handler{
		pool:      pool,
		targetURL: targetURL,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", h.Index)
	mux.HandleFunc("/ui", h.Index)
	mux.HandleFunc("/v1/chat/completions", h.ChatCompletions)
	mux.HandleFunc("/stats", h.Stats)
	mux.HandleFunc("/keys", h.Keys)
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

	h.stats.mu.Lock()
	h.stats.TotalRequests++
	h.stats.mu.Unlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	key, err := h.pool.GetKey()
	if err != nil {
		http.Error(w, "No API key available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	req, err := http.NewRequest(http.MethodPost, h.targetURL, bytes.NewReader(body))
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
		http.Error(w, "Failed to proxy request: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	h.pool.IncrementUsage(key)

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	h.stats.mu.RLock()
	proxyRequests := h.stats.TotalRequests
	h.stats.mu.RUnlock()

	total, enabled, disabled, _ := h.pool.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"proxy_requests": proxyRequests,
		"total_usage":    total,
		"enabled_keys":   enabled,
		"disabled_keys":  disabled,
	})
}

func (h *Handler) Keys(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		keys, err := h.pool.GetAll()
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
			Action string `json:"action"`
			Key    string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		var err error
		switch req.Action {
		case "add":
			err = h.pool.Add(req.Key)
		case "remove":
			err = h.pool.Remove(req.Key)
		case "enable":
			err = h.pool.Enable(req.Key)
		case "disable":
			err = h.pool.Disable(req.Key)
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