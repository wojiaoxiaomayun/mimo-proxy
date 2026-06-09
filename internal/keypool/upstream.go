package keypool

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	clientFingerprintSettingKey = "client_fingerprint"
	clientFingerprintHeader     = "X-Client-Fingerprint"
	upstreamUserAgent           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

func (h *Handler) upstreamClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

func (h *Handler) prepareUpstreamRequest(req *http.Request) {
	fingerprint := h.pool.GetOrCreateClientFingerprint()
	req.Header.Set(clientFingerprintHeader, fingerprint)
	setHeaderIfEmpty(req, "User-Agent", upstreamUserAgent)
	setHeaderIfEmpty(req, "Accept", "application/json, text/plain, */*")
	setHeaderIfEmpty(req, "Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	setHeaderIfEmpty(req, "Sec-CH-UA", `"Not/A)Brand";v="8", "Chromium";v="126", "Google Chrome";v="126"`)
	setHeaderIfEmpty(req, "Sec-CH-UA-Mobile", "?0")
	setHeaderIfEmpty(req, "Sec-CH-UA-Platform", `"Windows"`)
}

func setHeaderIfEmpty(req *http.Request, key, value string) {
	if req.Header.Get(key) == "" {
		req.Header.Set(key, value)
	}
}

func (p *KeyPool) GetOrCreateClientFingerprint() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	var fingerprint string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, clientFingerprintSettingKey).Scan(&fingerprint)
	if err == nil && strings.TrimSpace(fingerprint) != "" {
		return fingerprint
	}

	fingerprint = generateClientFingerprint()
	p.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, clientFingerprintSettingKey, fingerprint)
	return fingerprint
}

func generateClientFingerprint() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("mimo-%d", time.Now().UnixNano())
	}
	return "mimo-" + hex.EncodeToString(buffer)
}
