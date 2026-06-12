package keypool

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientFingerprintPersists(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	first := pool.GetOrCreateClientFingerprint()
	second := pool.GetOrCreateClientFingerprint()

	if first == "" {
		t.Fatal("fingerprint is empty")
	}
	if !strings.HasPrefix(first, "mimo-") {
		t.Fatalf("fingerprint = %q, want mimo-*", first)
	}
	if first != second {
		t.Fatalf("fingerprint changed: first=%q second=%q", first, second)
	}
}

func TestPrepareUpstreamRequestAddsFingerprintHeaders(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	handler := &Handler{pool: pool}
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	handler.prepareUpstreamRequest(req)

	fingerprint := req.Header.Get(clientFingerprintHeader)
	if fingerprint == "" {
		t.Fatalf("%s header is empty", clientFingerprintHeader)
	}
	if req.Header.Get("User-Agent") != upstreamUserAgent {
		t.Fatalf("User-Agent = %q", req.Header.Get("User-Agent"))
	}
	if strings.Contains(strings.ToLower(req.Header.Get("User-Agent")), "mimoproxy") {
		t.Fatalf("User-Agent exposes proxy name: %q", req.Header.Get("User-Agent"))
	}
	if req.Header.Get("X-Client-Name") != "" {
		t.Fatalf("X-Client-Name should not be set, got %q", req.Header.Get("X-Client-Name"))
	}
	if req.Header.Get("Sec-CH-UA-Platform") != `"Windows"` {
		t.Fatalf("Sec-CH-UA-Platform = %q", req.Header.Get("Sec-CH-UA-Platform"))
	}
	if req.Header.Get("Accept-Language") == "" {
		t.Fatal("Accept-Language is empty")
	}
	if pool.GetSetting(clientFingerprintSettingKey, "") != fingerprint {
		t.Fatal("fingerprint was not persisted in settings")
	}
}

func TestUpstreamClientForChannelNoProxy(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	h := &Handler{pool: pool}

	// nil channel → direct client, no error
	c, err := h.upstreamClientForChannel(nil, 5*time.Second)
	if err != nil {
		t.Fatalf("nil channel: err = %v", err)
	}
	if c.Timeout != 5*time.Second {
		t.Fatalf("nil channel: timeout = %v, want 5s", c.Timeout)
	}

	// channel with empty proxy → direct client
	c, err = h.upstreamClientForChannel(&ChannelInfo{ProxyURL: ""}, 7*time.Second)
	if err != nil {
		t.Fatalf("empty proxy: err = %v", err)
	}
	if c.Timeout != 7*time.Second {
		t.Fatalf("empty proxy: timeout = %v, want 7s", c.Timeout)
	}
}

func TestUpstreamClientForChannelHTTPProxy(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	h := &Handler{pool: pool}
	// http proxy URL must produce a non-nil client with transport configured
	c, err := h.upstreamClientForChannel(&ChannelInfo{ProxyURL: "http://127.0.0.1:8080"}, 3*time.Second)
	if err != nil {
		t.Fatalf("http proxy: err = %v", err)
	}
	if c.Timeout != 3*time.Second {
		t.Fatalf("http proxy: timeout = %v, want 3s", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("http proxy: Transport should be configured")
	}
}

func TestUpstreamClientForChannelSOCKS5Proxy(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	h := &Handler{pool: pool}
	// socks5h proxy URL with user:pass auth
	c, err := h.upstreamClientForChannel(&ChannelInfo{ProxyURL: "socks5h://user:pass@127.0.0.1:1080"}, 2*time.Second)
	if err != nil {
		t.Fatalf("socks5h proxy: err = %v", err)
	}
	if c.Timeout != 2*time.Second {
		t.Fatalf("socks5h proxy: timeout = %v, want 2s", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("socks5h proxy: Transport should be configured")
	}
}

func TestUpstreamClientForChannelInvalidProxyFallsBackToDirect(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	h := &Handler{pool: pool}

	// Unsupported scheme → error but client is still a working direct client.
	c, err := h.upstreamClientForChannel(&ChannelInfo{ProxyURL: "ftp://127.0.0.1:21"}, 1*time.Second)
	if err == nil {
		t.Fatal("expected error for unsupported proxy scheme, got nil")
	}
	if c == nil {
		t.Fatal("expected fallback client to be non-nil")
	}

	// Malformed URL → error but client is still a working direct client.
	c, err = h.upstreamClientForChannel(&ChannelInfo{ProxyURL: "://no-scheme"}, 1*time.Second)
	if err == nil {
		t.Fatal("expected error for malformed proxy url, got nil")
	}
	if c == nil {
		t.Fatal("expected fallback client to be non-nil")
	}
}

func TestUpstreamClientForProxyURLWrapper(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	h := &Handler{pool: pool}
	c, err := h.upstreamClientForProxyURL("socks5://127.0.0.1:1080", 4*time.Second)
	if err != nil {
		t.Fatalf("socks5 wrapper: err = %v", err)
	}
	if c.Timeout != 4*time.Second {
		t.Fatalf("socks5 wrapper: timeout = %v, want 4s", c.Timeout)
	}
}
