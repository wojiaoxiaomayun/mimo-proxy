package keypool

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	Prefix        string   `json:"prefix"`
	BaseURL       string   `json:"base_url"`
	WebsiteURL    string   `json:"website_url"`
	ChannelType   string   `json:"channel_type"` // "openai" or "anthropic"
	Enabled       bool     `json:"enabled"`
	IsDefault     bool     `json:"is_default"`
	PinnedKey     string   `json:"pinned_key"`
	KeyMode       string   `json:"key_mode"`
	AllowedModels []string `json:"allowed_models"`
	CreatedAt     string   `json:"created_at"`
}

type KeyPool struct {
	db *sql.DB
	mu sync.RWMutex
}

type BackupData struct {
	Version       int               `json:"version"`
	ExportedAt    string            `json:"exported_at"`
	Channels      []ChannelInfo     `json:"channels"`
	Keys          []KeyInfo         `json:"keys"`
	ProxyKeys     []ProxyKeyInfo    `json:"proxy_keys"`
	ModelMappings []ModelMapping    `json:"model_mappings"`
	Settings      map[string]string `json:"settings"`
}

type ImportSummary struct {
	Channels      int `json:"channels"`
	Keys          int `json:"keys"`
	ProxyKeys     int `json:"proxy_keys"`
	ModelMappings int `json:"model_mappings"`
	Settings      int `json:"settings"`
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func defaultCreatedAt(createdAt string) string {
	if strings.TrimSpace(createdAt) != "" {
		return createdAt
	}
	return time.Now().Format("2006-01-02 15:04:05")
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
		"ALTER TABLE channels ADD COLUMN pinned_key TEXT DEFAULT ''",
		"ALTER TABLE channels ADD COLUMN key_mode TEXT DEFAULT 'round-robin'",
		"ALTER TABLE channels ADD COLUMN failover_key TEXT DEFAULT ''",
		"ALTER TABLE channels ADD COLUMN channel_type TEXT DEFAULT 'openai'",
		"ALTER TABLE request_logs ADD COLUMN request_body TEXT DEFAULT ''",
		"ALTER TABLE request_logs ADD COLUMN response_body TEXT DEFAULT ''",
		"ALTER TABLE channels ADD COLUMN allowed_models TEXT DEFAULT '[]'",
		"ALTER TABLE channels ADD COLUMN website_url TEXT DEFAULT ''",
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
		CREATE TABLE IF NOT EXISTS model_mappings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			channel_id INTEGER NOT NULL,
			target_model TEXT NOT NULL DEFAULT '',
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

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_usage (
			date TEXT PRIMARY KEY,
			calls INTEGER DEFAULT 0,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0
		)
	`)
	if err != nil {
		return nil, err
	}

	var dailyRows int
	db.QueryRow(`SELECT COUNT(*) FROM daily_usage`).Scan(&dailyRows)
	if dailyRows == 0 {
		db.Exec(`
			INSERT INTO daily_usage (date, calls, prompt_tokens, completion_tokens, total_tokens)
			SELECT date(created_at, 'localtime'), COUNT(*), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0)
			FROM request_logs
			WHERE path LIKE '%chat/completions%'
			GROUP BY date(created_at, 'localtime')
		`)
	}

	return &KeyPool{db: db}, nil
}

func (p *KeyPool) GetKey(channelID int) (key string, defaultModel string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var pinnedKey, keyMode string
	p.db.QueryRow(`SELECT COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin') FROM channels WHERE id = ?`, channelID).Scan(&pinnedKey, &keyMode)

	// Pinned key always takes priority (manual pin overrides mode).
	if pinnedKey != "" {
		var dm string
		var enabled bool
		if err := p.db.QueryRow(`SELECT COALESCE(default_model,''), enabled FROM api_keys WHERE key = ?`, pinnedKey).Scan(&dm, &enabled); err == nil && enabled {
			return pinnedKey, dm, nil
		}
	}

	// Failover mode: prefer stored failover_key.
	if keyMode == "failover" {
		var fk string
		p.db.QueryRow(`SELECT COALESCE(failover_key,'') FROM channels WHERE id = ?`, channelID).Scan(&fk)
		if fk != "" {
			var dm string
			var enabled bool
			if err := p.db.QueryRow(`SELECT COALESCE(default_model,''), enabled FROM api_keys WHERE key = ?`, fk).Scan(&dm, &enabled); err == nil && enabled {
				return fk, dm, nil
			}
		}
	}

	// Round-robin (or failover fallback): least-used enabled key.
	var dm string
	err = p.db.QueryRow(`
		SELECT key, COALESCE(default_model,'') FROM api_keys
		WHERE enabled = 1 AND channel_id = ?
		ORDER BY usage_count ASC, id ASC
		LIMIT 1
	`, channelID).Scan(&key, &dm)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", "", fmt.Errorf("no available API key for channel %d", channelID)
		}
		return "", "", err
	}

	// Persist failover key so subsequent requests use the same one.
	if keyMode == "failover" {
		p.db.Exec(`UPDATE channels SET failover_key = ? WHERE id = ?`, key, channelID)
	}
	return key, dm, nil
}

// RotateFailoverKey disables the given key and clears the failover_key for the channel.
// Called when a key fails in failover mode, forcing the next request to pick a new key.
func (p *KeyPool) RotateFailoverKey(channelID int, failedKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`UPDATE api_keys SET enabled = 0 WHERE key = ?`, failedKey)
	p.db.Exec(`UPDATE channels SET failover_key = '' WHERE id = ?`, channelID)
}

// SetKeyMode sets the key selection mode for a channel.
func (p *KeyPool) SetKeyMode(channelID int, mode string) error {
	if mode != "round-robin" && mode != "failover" {
		return fmt.Errorf("invalid key mode: %s", mode)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET key_mode = ? WHERE id = ?`, mode, channelID)
	return err
}

// PinKey sets a key as the preferred key for a channel.
func (p *KeyPool) PinKey(channelID int, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET pinned_key = ? WHERE id = ?`, key, channelID)
	return err
}

// UnpinKey removes the pinned key for a channel.
func (p *KeyPool) UnpinKey(channelID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET pinned_key = '' WHERE id = ?`, channelID)
	return err
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
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
		INSERT INTO daily_usage (date, calls, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, 1, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			calls = calls + 1,
			prompt_tokens = prompt_tokens + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens,
			total_tokens = total_tokens + excluded.total_tokens
	`, time.Now().Format("2006-01-02"), promptTokens, completionTokens, totalTokens)
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
	return p.queryKeys(channelID)
}

func (p *KeyPool) queryKeys(channelID int) ([]KeyInfo, error) {
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

func (p *KeyPool) GetStats() (totalTokens int64, totalCalls int64, todayTokens int64, todayCalls int64, enabledCount, disabledCount int, err error) {
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

	err = p.db.QueryRow(`SELECT COALESCE(SUM(calls), 0), COALESCE(SUM(total_tokens), 0) FROM daily_usage WHERE date = ?`, time.Now().Format("2006-01-02")).Scan(&todayCalls, &todayTokens)
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

// ExportBackup returns configuration data only; request logs are intentionally excluded.
func (p *KeyPool) ExportBackup() (*BackupData, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	backup := &BackupData{
		Version:    1,
		ExportedAt: time.Now().Format(time.RFC3339),
		Settings:   make(map[string]string),
	}

	channels, err := p.queryChannels()
	if err != nil {
		return nil, err
	}
	backup.Channels = channels

	keys, err := p.queryKeys(0)
	if err != nil {
		return nil, err
	}
	backup.Keys = keys

	proxyKeys, err := p.queryProxyKeys()
	if err != nil {
		return nil, err
	}
	backup.ProxyKeys = proxyKeys

	modelMappings, err := p.queryModelMappings()
	if err != nil {
		return nil, err
	}
	backup.ModelMappings = modelMappings

	rows, err := p.db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		backup.Settings[key] = value
	}

	return backup, rows.Err()
}

func (p *KeyPool) ImportBackup(backup *BackupData) (*ImportSummary, error) {
	if backup == nil {
		return nil, fmt.Errorf("backup is empty")
	}
	if len(backup.Channels) == 0 && len(backup.Keys) == 0 && len(backup.ProxyKeys) == 0 && len(backup.ModelMappings) == 0 && len(backup.Settings) == 0 {
		return nil, fmt.Errorf("backup contains no importable data")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	summary := &ImportSummary{}
	channelIDMap := make(map[int]int)
	defaultImported := false
	for _, channel := range backup.Channels {
		prefix := strings.TrimSpace(channel.Prefix)
		if prefix == "" {
			return nil, fmt.Errorf("channel prefix is required")
		}
		keyMode := channel.KeyMode
		if keyMode == "" {
			keyMode = "round-robin"
		}
		if keyMode != "round-robin" && keyMode != "failover" {
			keyMode = "round-robin"
		}
		channelType := channel.ChannelType
		if channelType == "" {
			channelType = "openai"
		}
		isDefault := channel.IsDefault && !defaultImported
		if channel.IsDefault && !defaultImported {
			if _, err := tx.Exec(`UPDATE channels SET is_default = 0`); err != nil {
				return nil, err
			}
			defaultImported = true
		}

		amJSON, _ := json.Marshal(channel.AllowedModels)
		_, err := tx.Exec(`
			INSERT INTO channels (name, prefix, base_url, website_url, channel_type, enabled, is_default, pinned_key, key_mode, allowed_models, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(prefix) DO UPDATE SET
				name = excluded.name,
				base_url = excluded.base_url,
				website_url = excluded.website_url,
				channel_type = excluded.channel_type,
				enabled = excluded.enabled,
				is_default = excluded.is_default,
				pinned_key = excluded.pinned_key,
				key_mode = excluded.key_mode,
				allowed_models = excluded.allowed_models
		`, channel.Name, prefix, channel.BaseURL, channel.WebsiteURL, channelType, boolToInt(channel.Enabled), boolToInt(isDefault), channel.PinnedKey, keyMode, string(amJSON), defaultCreatedAt(channel.CreatedAt))
		if err != nil {
			return nil, err
		}

		var newID int
		if err := tx.QueryRow(`SELECT id FROM channels WHERE prefix = ?`, prefix).Scan(&newID); err != nil {
			return nil, err
		}
		channelIDMap[channel.ID] = newID
		summary.Channels++
	}

	defaultChannelID := 0
	tx.QueryRow(`SELECT id FROM channels WHERE is_default = 1 ORDER BY id LIMIT 1`).Scan(&defaultChannelID)
	if defaultChannelID == 0 {
		tx.QueryRow(`SELECT id FROM channels ORDER BY id LIMIT 1`).Scan(&defaultChannelID)
	}

	for _, key := range backup.Keys {
		apiKey := strings.TrimSpace(key.Key)
		if apiKey == "" {
			continue
		}
		channelID := channelIDMap[key.ChannelID]
		if channelID == 0 {
			channelID = defaultChannelID
		}
		_, err := tx.Exec(`
			INSERT INTO api_keys (key, note, channel_id, default_model, usage_count, prompt_tokens, completion_tokens, total_tokens, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				note = excluded.note,
				channel_id = excluded.channel_id,
				default_model = excluded.default_model,
				usage_count = excluded.usage_count,
				prompt_tokens = excluded.prompt_tokens,
				completion_tokens = excluded.completion_tokens,
				total_tokens = excluded.total_tokens,
				enabled = excluded.enabled
		`, apiKey, key.Note, channelID, key.DefaultModel, key.UsageCount, key.PromptTokens, key.CompletionTokens, key.TotalTokens, boolToInt(key.Enabled), defaultCreatedAt(key.CreatedAt))
		if err != nil {
			return nil, err
		}
		summary.Keys++
	}

	for _, proxyKey := range backup.ProxyKeys {
		key := strings.TrimSpace(proxyKey.Key)
		if key == "" {
			continue
		}
		_, err := tx.Exec(`
			INSERT INTO proxy_keys (key, note, enabled, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				note = excluded.note,
				enabled = excluded.enabled
		`, key, proxyKey.Note, boolToInt(proxyKey.Enabled), defaultCreatedAt(proxyKey.CreatedAt))
		if err != nil {
			return nil, err
		}
		summary.ProxyKeys++
	}

	for _, mm := range backup.ModelMappings {
		name := strings.TrimSpace(mm.Name)
		if name == "" {
			continue
		}
		channelID := channelIDMap[mm.ChannelID]
		if channelID == 0 {
			channelID = defaultChannelID
		}
		_, err := tx.Exec(`
			INSERT INTO model_mappings (name, channel_id, target_model, note, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				channel_id = excluded.channel_id,
				target_model = excluded.target_model,
				note = excluded.note,
				enabled = excluded.enabled
		`, name, channelID, mm.TargetModel, mm.Note, boolToInt(mm.Enabled), defaultCreatedAt(mm.CreatedAt))
		if err != nil {
			return nil, err
		}
		summary.ModelMappings++
	}

	for key, value := range backup.Settings {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return nil, err
		}
		summary.Settings++
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return summary, nil
}

// ============ Channels ============

func (p *KeyPool) GetAllChannels() ([]ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.queryChannels()
}

func (p *KeyPool) queryChannels() ([]ChannelInfo, error) {
	rows, err := p.db.Query(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []ChannelInfo
	for rows.Next() {
		var c ChannelInfo
		var amStr string
		if err := rows.Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(amStr), &c.AllowedModels)
		channels = append(channels, c)
	}
	return channels, nil
}

func (p *KeyPool) queryModelMappings() ([]ModelMapping, error) {
	rows, err := p.db.Query(`
		SELECT m.id, m.name, m.channel_id, m.target_model, COALESCE(m.note,''), m.enabled, m.created_at
		FROM model_mappings m
		ORDER BY m.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mappings []ModelMapping
	for rows.Next() {
		var m ModelMapping
		if err := rows.Scan(&m.ID, &m.Name, &m.ChannelID, &m.TargetModel, &m.Note, &m.Enabled, &m.CreatedAt); err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}
	return mappings, nil
}

func (p *KeyPool) GetChannelByPrefix(prefix string) (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	var amStr string
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels WHERE prefix = ? AND enabled = 1`, prefix).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(amStr), &c.AllowedModels)
	return &c, nil
}

func (p *KeyPool) GetDefaultChannel() (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	var amStr string
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels WHERE is_default = 1 AND enabled = 1`).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt)
	if err != nil {
		// Fallback: first enabled channel
		err = p.db.QueryRow(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels WHERE enabled = 1 ORDER BY id LIMIT 1`).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt)
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(amStr), &c.AllowedModels)
	return &c, nil
}

func (p *KeyPool) GetChannelByID(id int) (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	var amStr string
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(amStr), &c.AllowedModels)
	return &c, nil
}

func (p *KeyPool) AddChannel(name, prefix, baseURL, websiteURL, channelType string, allowedModels []string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return 0, fmt.Errorf("prefix is required")
	}
	if channelType == "" {
		channelType = "openai"
	}
	amJSON, _ := json.Marshal(allowedModels)
	res, err := p.db.Exec(`INSERT INTO channels (name, prefix, base_url, website_url, channel_type, allowed_models) VALUES (?, ?, ?, ?, ?, ?)`, name, prefix, baseURL, websiteURL, channelType, string(amJSON))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(id), nil
}

func (p *KeyPool) SetDefaultChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`UPDATE channels SET is_default = 0`)
	_, err := p.db.Exec(`UPDATE channels SET is_default = 1 WHERE id = ?`, id)
	return err
}

func (p *KeyPool) UpdateChannel(id int, name, prefix, baseURL, websiteURL, channelType string, allowedModels []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	if channelType == "" {
		channelType = "openai"
	}
	amJSON, _ := json.Marshal(allowedModels)
	_, err := p.db.Exec(`UPDATE channels SET name = ?, prefix = ?, base_url = ?, website_url = ?, channel_type = ?, allowed_models = ? WHERE id = ?`, name, prefix, baseURL, websiteURL, channelType, string(amJSON), id)
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
	return p.queryProxyKeys()
}

func (p *KeyPool) queryProxyKeys() ([]ProxyKeyInfo, error) {
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

// ============ Model Mappings ============

type ModelMapping struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ChannelID   int    `json:"channel_id"`
	TargetModel string `json:"target_model"`
	Note        string `json:"note"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	// Enriched fields (from join)
	ChannelName string `json:"channel_name,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
}

func (p *KeyPool) GetAllModelMappings() ([]ModelMapping, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rows, err := p.db.Query(`
		SELECT m.id, m.name, m.channel_id, m.target_model, COALESCE(m.note,''), m.enabled, m.created_at,
		       COALESCE(c.name,''), COALESCE(c.channel_type,'openai')
		FROM model_mappings m
		LEFT JOIN channels c ON c.id = m.channel_id
		ORDER BY m.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mappings []ModelMapping
	for rows.Next() {
		var m ModelMapping
		if err := rows.Scan(&m.ID, &m.Name, &m.ChannelID, &m.TargetModel, &m.Note, &m.Enabled, &m.CreatedAt, &m.ChannelName, &m.ChannelType); err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}
	return mappings, nil
}

func (p *KeyPool) AddModelMapping(name string, channelID int, targetModel, note string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`INSERT INTO model_mappings (name, channel_id, target_model, note) VALUES (?, ?, ?, ?)`, name, channelID, targetModel, note)
	return err
}

func (p *KeyPool) UpdateModelMapping(id int, name string, channelID int, targetModel, note string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE model_mappings SET name = ?, channel_id = ?, target_model = ?, note = ? WHERE id = ?`, name, channelID, targetModel, note, id)
	return err
}

func (p *KeyPool) RemoveModelMapping(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`DELETE FROM model_mappings WHERE id = ?`, id)
	return err
}

// ResolveModelMapping looks up a model name in model_mappings.
// Returns (channelID, targetModel, true) if found and enabled, or (0, "", false) if not a mapping.
func (p *KeyPool) ResolveModelMapping(modelName string) (int, string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var channelID int
	var targetModel string
	err := p.db.QueryRow(`SELECT channel_id, target_model FROM model_mappings WHERE name = ? AND enabled = 1`, modelName).Scan(&channelID, &targetModel)
	if err != nil || channelID == 0 {
		return 0, "", false
	}
	return channelID, targetModel, true
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
