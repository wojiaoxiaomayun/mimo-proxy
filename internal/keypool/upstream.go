package keypool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const (
	clientFingerprintSettingKey = "client_fingerprint"
	clientFingerprintHeader     = "X-Client-Fingerprint"
	upstreamUserAgent           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// getOrCreateTransport returns a cached *http.Transport for the given proxy URL.
// An empty proxyURL means direct connection. Transports are cached by proxy URL
// to reuse TCP connections across requests.
func (h *Handler) getOrCreateTransport(proxyURL string) (*http.Transport, error) {
	// Fast path: check cache under read lock.
	h.transportMu.RLock()
	if t, ok := h.transportCache[proxyURL]; ok {
		h.transportMu.RUnlock()
		return t, nil
	}
	h.transportMu.RUnlock()

	// Slow path: build transport under write lock.
	h.transportMu.Lock()
	defer h.transportMu.Unlock()

	// Double-check after acquiring write lock.
	if t, ok := h.transportCache[proxyURL]; ok {
		return t, nil
	}

	transport := &http.Transport{
		MaxIdleConns:        TransportMaxIdleConns,
		MaxIdleConnsPerHost: TransportMaxIdleConnsPerHost,
		IdleConnTimeout:     TransportIdleConnTimeout,
		DialContext:         defaultDialContext(0),
	}

	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid proxy url: %s", proxyURL)
		}

		scheme := strings.ToLower(parsed.Scheme)
		switch scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if parsed.User != nil {
				password, _ := parsed.User.Password()
				auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
			}
			dialer, derr := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
			if derr != nil {
				return nil, fmt.Errorf("socks5 proxy dialer: %w", derr)
			}
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		default:
			return nil, fmt.Errorf("unsupported proxy scheme: %s", scheme)
		}
	}

	h.transportCache[proxyURL] = transport
	return transport, nil
}

// upstreamClientForChannel returns an *http.Client that uses the channel's
// configured proxy URL (if any) and the given timeout. An empty ProxyURL
// means "no proxy" (direct connection). Supported proxy schemes:
//
//	http://[user:pass@]host:port   — HTTP proxy
//	https://[user:pass@]host:port  — HTTPS proxy (CONNECT)
//	socks5://[user:pass@]host:port — SOCKS5 proxy
//	socks5h://[user:pass@]host:port — SOCKS5 proxy (remote DNS)
//
// If the proxy URL is malformed the function falls back to a direct client
// (no proxy) and returns the error to the caller for logging.
//
// Transports are cached per proxy URL so TCP connections are reused across requests.
func (h *Handler) upstreamClientForChannel(channel *ChannelInfo, timeout time.Duration) (*http.Client, error) {
	var proxyURL string
	if channel != nil {
		proxyURL = strings.TrimSpace(channel.ProxyURL)
	}

	transport, err := h.getOrCreateTransport(proxyURL)
	if err != nil {
		// Fallback to direct connection on malformed proxy URL.
		directTransport, _ := h.getOrCreateTransport("")
		return &http.Client{Timeout: timeout, Transport: directTransport}, err
	}

	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// defaultDialContext returns a DialContext with a sensible per-connection
// timeout derived from the request timeout.
func defaultDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialTimeout := 10 * time.Second
	if timeout > 0 && timeout < dialTimeout {
		dialTimeout = timeout
	}
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return d.DialContext
}

// upstreamClientForProxyURL is a small wrapper used by endpoints that don't
// carry a full *ChannelInfo (e.g. when the user supplies a one-off base URL
// via a query param). It accepts the proxy URL directly.
func (h *Handler) upstreamClientForProxyURL(proxyURL string, timeout time.Duration) (*http.Client, error) {
	return h.upstreamClientForChannel(&ChannelInfo{ProxyURL: proxyURL}, timeout)
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
