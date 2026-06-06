package keypool

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type KeyInfo struct {
	ID               int    `json:"id"`
	Key              string `json:"key"`
	ChannelID        int    `json:"channel_id"`
	Note             string `json:"note"`
	DefaultModel     string `json:"default_model"`
	UsageCount       int    `json:"usage_count"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Enabled          bool   `json:"enabled"`
	CreatedAt        string `json:"created_at"`
}

type ChannelInfo struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	BaseURL   string `json:"base_url"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"is_default"`
	CreatedAt string `json:"created_at"`
}

type KeyPool struct {
	db *sql.DB
	mu sync.RWMutex
}

func New(dbPath, defaultURL string) (*KeyPool, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT UNIQUE NOT NULL,
			usage_count INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			prefix TEXT UNIQUE NOT NULL,
			base_url TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			is_default INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	// Ensure at least one default channel exists.
	var chCount int
	db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&chCount)
	if chCount == 0 {
		db.Exec(`INSERT INTO channels (name, prefix, base_url, is_default) VALUES ('默认', 'default', ?, 1)`, defaultURL)
	}

	// Migrate: add columns if missing.
	for _, col := range []string{
		"ALTER TABLE api_keys ADD COLUMN prompt_tokens INTEGER DEFAULT 0",
		"ALTER TABLE api_keys ADD COLUMN completion_tokens INTEGER DEFAULT 0",
		"ALTER TABLE api_keys ADD COLUMN total_tokens INTEGER DEFAULT 0",
		"ALTER TABLE api_keys ADD COLUMN note TEXT DEFAULT ''",
		"ALTER TABLE api_keys ADD COLUMN channel_id INTEGER DEFAULT 0",
		"ALTER TABLE api_keys ADD COLUMN default_model TEXT DEFAULT ''",
		"ALTER TABLE channels ADD COLUMN is_default INTEGER DEFAULT 0",
		"ALTER TABLE request_logs ADD COLUMN request_body TEXT DEFAULT ''",
		"ALTER TABLE request_logs ADD COLUMN response_body TEXT DEFAULT ''",
	} {
		db.Exec(col) // ignore duplicate-column errors
	}

	// Migrate existing keys to default channel (channel_id=0 → first channel).
	var defaultChID int
	db.QueryRow(`SELECT id FROM channels ORDER BY id LIMIT 1`).Scan(&defaultChID)
	db.Exec(`UPDATE api_keys SET channel_id = ? WHERE channel_id = 0`, defaultChID)

	// Migrate: fix empty-prefix channels and ensure a default exists.
	db.Exec(`UPDATE channels SET prefix = 'default' WHERE prefix = ''`)
	var hasDefault int
	db.QueryRow(`SELECT COUNT(*) FROM channels WHERE is_default = 1`).Scan(&hasDefault)
	if hasDefault == 0 {
		db.Exec(`UPDATE channels SET is_default = 1 WHERE id = (SELECT MIN(id) FROM channels)`)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS proxy_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT UNIQUE NOT NULL,
			note TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			method TEXT,
			path TEXT,
			status_code INTEGER,
			latency_ms INTEGER,
			proxy_key TEXT,
			upstream_key TEXT,
			model TEXT,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			error TEXT DEFAULT '',
			request_body TEXT DEFAULT '',
			response_body TEXT DEFAULT ''
		)
	`)
	if err != nil {
		return nil, err
	}

	return &KeyPool{db: db}, nil
}

func (p *KeyPool) GetKey(channelID int) (key string, defaultModel string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var enabled bool
	err = p.db.QueryRow(`
		SELECT key, COALESCE(default_model,''), enabled FROM api_keys 
		WHERE enabled = 1 AND channel_id = ?
		ORDER BY usage_count ASC, id ASC 
		LIMIT 1
	`, channelID).Scan(&key, &defaultModel, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", "", fmt.Errorf("no available API key for channel %d", channelID)
		}
		return "", "", err
	}
	return key, defaultModel, nil
}

func (p *KeyPool) IncrementUsage(key string, promptTokens, completionTokens, totalTokens int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`
		UPDATE api_keys SET
			usage_count = usage_count + 1,
			prompt_tokens = prompt_tokens + ?,
			completion_tokens = completion_tokens + ?,
			total_tokens = total_tokens + ?
		WHERE key = ?
	`, promptTokens, completionTokens, totalTokens, key)
	return err
}

func (p *KeyPool) Add(key, note string, channelID int, defaultModel string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`INSERT OR IGNORE INTO api_keys (key, note, channel_id, default_model) VALUES (?, ?, ?, ?)`, key, note, channelID, defaultModel)
	return err
}

func (p *KeyPool) Remove(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`DELETE FROM api_keys WHERE key = ?`, key)
	return err
}

func (p *KeyPool) Disable(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE api_keys SET enabled = 0 WHERE key = ?`, key)
	return err
}

func (p *KeyPool) Enable(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE api_keys SET enabled = 1 WHERE key = ?`, key)
	return err
}

func (p *KeyPool) UpdateNote(key, note string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE api_keys SET note = ? WHERE key = ?`, note, key)
	return err
}

func (p *KeyPool) UpdateKeyDefaultModel(key, defaultModel string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE api_keys SET default_model = ? WHERE key = ?`, defaultModel, key)
	return err
}

func (p *KeyPool) UpdateKey(key, note, defaultModel string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE api_keys SET note = ?, default_model = ? WHERE key = ?`, note, defaultModel, key)
	return err
}

func (p *KeyPool) GetAll(channelID int) ([]KeyInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var rows *sql.Rows
	var err error
	if channelID > 0 {
		rows, err = p.db.Query(`SELECT id, key, channel_id, note, COALESCE(default_model,''), usage_count, prompt_tokens, completion_tokens, total_tokens, enabled, created_at FROM api_keys WHERE channel_id = ? ORDER BY id`, channelID)
	} else {
		rows, err = p.db.Query(`SELECT id, key, channel_id, note, COALESCE(default_model,''), usage_count, prompt_tokens, completion_tokens, total_tokens, enabled, created_at FROM api_keys ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.Key, &k.ChannelID, &k.Note, &k.DefaultModel, &k.UsageCount, &k.PromptTokens, &k.CompletionTokens, &k.TotalTokens, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (p *KeyPool) GetStats() (totalTokens int64, totalCalls int64, enabledCount, disabledCount int, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	err = p.db.QueryRow(`SELECT COALESCE(SUM(total_tokens), 0) FROM api_keys`).Scan(&totalTokens)
	if err != nil {
		return
	}

	err = p.db.QueryRow(`SELECT COALESCE(SUM(usage_count), 0) FROM api_keys`).Scan(&totalCalls)
	if err != nil {
		return
	}

	err = p.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE enabled = 1`).Scan(&enabledCount)
	if err != nil {
		return
	}

	err = p.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE enabled = 0`).Scan(&disabledCount)
	return
}

func (p *KeyPool) LoadFromEnv(keysStr string) error {
	ch, _ := p.GetDefaultChannel()
	cid := 0
	if ch != nil {
		cid = ch.ID
	}
	keys := strings.Split(keysStr, ",")
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			if err := p.Add(key, "", cid, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *KeyPool) Close() error {
	return p.db.Close()
}

// ============ Channels ============

func (p *KeyPool) GetAllChannels() ([]ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rows, err := p.db.Query(`SELECT id, name, prefix, base_url, enabled, is_default, created_at FROM channels ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []ChannelInfo
	for rows.Next() {
		var c ChannelInfo
		if err := rows.Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.Enabled, &c.IsDefault, &c.CreatedAt); err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, nil
}

func (p *KeyPool) GetChannelByPrefix(prefix string) (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, enabled, is_default, created_at FROM channels WHERE prefix = ? AND enabled = 1`, prefix).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.Enabled, &c.IsDefault, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *KeyPool) GetDefaultChannel() (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, enabled, is_default, created_at FROM channels WHERE is_default = 1 AND enabled = 1`).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.Enabled, &c.IsDefault, &c.CreatedAt)
	if err != nil {
		// Fallback: first enabled channel
		err = p.db.QueryRow(`SELECT id, name, prefix, base_url, enabled, is_default, created_at FROM channels WHERE enabled = 1 ORDER BY id LIMIT 1`).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.Enabled, &c.IsDefault, &c.CreatedAt)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *KeyPool) AddChannel(name, prefix, baseURL string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	_, err := p.db.Exec(`INSERT INTO channels (name, prefix, base_url) VALUES (?, ?, ?)`, name, prefix, baseURL)
	return err
}

func (p *KeyPool) SetDefaultChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`UPDATE channels SET is_default = 0`)
	_, err := p.db.Exec(`UPDATE channels SET is_default = 1 WHERE id = ?`, id)
	return err
}

func (p *KeyPool) UpdateChannel(id int, name, prefix, baseURL string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	_, err := p.db.Exec(`UPDATE channels SET name = ?, prefix = ?, base_url = ? WHERE id = ?`, name, prefix, baseURL, id)
	return err
}

func (p *KeyPool) RemoveChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

func (p *KeyPool) EnableChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET enabled = 1 WHERE id = ?`, id)
	return err
}

func (p *KeyPool) DisableChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET enabled = 0 WHERE id = ?`, id)
	return err
}

// ============ Proxy Keys (sk-xxx) ============

type ProxyKeyInfo struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

// GenerateProxyKey creates a new random sk-xxx key and stores it.
func (p *KeyPool) GenerateProxyKey(note string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := make([]byte, 24)
	_, err := rand.Read(b)
	if err != nil {
		return "", fmt.Errorf("failed to generate random key: %w", err)
	}
	key := fmt.Sprintf("sk-%x", b)
	_, err = p.db.Exec(`INSERT INTO proxy_keys (key, note) VALUES (?, ?)`, key, note)
	return key, err
}

func (p *KeyPool) ValidateProxyKey(key string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var enabled bool
	err := p.db.QueryRow(`SELECT enabled FROM proxy_keys WHERE key = ?`, key).Scan(&enabled)
	return err == nil && enabled
}

func (p *KeyPool) GetAllProxyKeys() ([]ProxyKeyInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rows, err := p.db.Query(`SELECT id, key, note, enabled, created_at FROM proxy_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []ProxyKeyInfo
	for rows.Next() {
		var k ProxyKeyInfo
		if err := rows.Scan(&k.ID, &k.Key, &k.Note, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (p *KeyPool) RemoveProxyKey(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`DELETE FROM proxy_keys WHERE key = ?`, key)
	return err
}

func (p *KeyPool) EnableProxyKey(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE proxy_keys SET enabled = 1 WHERE key = ?`, key)
	return err
}

func (p *KeyPool) DisableProxyKey(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE proxy_keys SET enabled = 0 WHERE key = ?`, key)
	return err
}

// ============ Request Logs ============

type RequestLog struct {
	ID               int    `json:"id"`
	CreatedAt        string `json:"created_at"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	StatusCode       int    `json:"status_code"`
	LatencyMs        int64  `json:"latency_ms"`
	ProxyKey         string `json:"proxy_key"`
	UpstreamKey      string `json:"upstream_key"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Error            string `json:"error"`
	RequestBody      string `json:"request_body,omitempty"`
	ResponseBody     string `json:"response_body,omitempty"`
}

func (p *KeyPool) LogRequest(l *RequestLog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`INSERT INTO request_logs
		(method, path, status_code, latency_ms, proxy_key, upstream_key, model, prompt_tokens, completion_tokens, total_tokens, error, request_body, response_body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Method, l.Path, l.StatusCode, l.LatencyMs, l.ProxyKey, l.UpstreamKey, l.Model,
		l.PromptTokens, l.CompletionTokens, l.TotalTokens, l.Error, l.RequestBody, l.ResponseBody)
}

func (p *KeyPool) GetLogs(page, pageSize int) ([]RequestLog, int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	var total int
	p.db.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&total)

	rows, err := p.db.Query(`SELECT id, created_at, method, path, status_code, latency_ms, proxy_key, upstream_key, model, prompt_tokens, completion_tokens, total_tokens, error FROM request_logs ORDER BY id DESC LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	var logs []RequestLog
	for rows.Next() {
		var l RequestLog
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.Method, &l.Path, &l.StatusCode, &l.LatencyMs, &l.ProxyKey, &l.UpstreamKey, &l.Model, &l.PromptTokens, &l.CompletionTokens, &l.TotalTokens, &l.Error); err != nil {
			return nil, total, err
		}
		logs = append(logs, l)
	}
	return logs, total, nil
}

func (p *KeyPool) CleanLogs() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`DELETE FROM request_logs WHERE created_at < datetime('now', '-1 hour')`)
}

func (p *KeyPool) GetLogDetail(id int) (*RequestLog, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var l RequestLog
	err := p.db.QueryRow(`SELECT id, created_at, method, path, status_code, latency_ms, proxy_key, upstream_key, model, prompt_tokens, completion_tokens, total_tokens, error, request_body, response_body FROM request_logs WHERE id = ?`, id).Scan(
		&l.ID, &l.CreatedAt, &l.Method, &l.Path, &l.StatusCode, &l.LatencyMs, &l.ProxyKey, &l.UpstreamKey, &l.Model, &l.PromptTokens, &l.CompletionTokens, &l.TotalTokens, &l.Error, &l.RequestBody, &l.ResponseBody)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// ============ Settings ============
// Silently ignores errors (e.g. table not yet created).
func (p *KeyPool) GetSetting(key, defaultValue string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var val string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		return defaultValue
	}
	if val == "" {
		return defaultValue
	}
	return val
}

// SetSetting upserts a setting value.
func (p *KeyPool) SetSetting(key, value string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

