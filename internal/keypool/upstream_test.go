package keypool

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
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
