package keypool

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ─── Data types ───────────────────────────────────────────────

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
	ChannelType   string   `json:"channel_type"`
	ProxyURL      string   `json:"proxy_url"`
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

	proxyKeyCache       map[string]bool
	proxyKeyCacheLoaded bool

	channelCache        map[string]*ChannelInfo
	defaultChannelCache *ChannelInfo
	channelCacheLoaded  bool
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

type ModelMapping struct {
	ID          int                  `json:"id"`
	Name        string               `json:"name"`
	ChannelType string               `json:"channel_type"`
	Strategy    string               `json:"strategy"`
	Note        string               `json:"note"`
	Enabled     bool                 `json:"enabled"`
	Targets     []ModelMappingTarget `json:"targets"`
	ChannelID   int                  `json:"channel_id,omitempty"`
	TargetModel string               `json:"target_model,omitempty"`
	ChannelName string               `json:"channel_name,omitempty"`
	CreatedAt   string               `json:"created_at"`
}

type ModelMappingTarget struct {
	ID          int    `json:"id"`
	MappingID   int    `json:"mapping_id"`
	ChannelID   int    `json:"channel_id"`
	TargetModel string `json:"target_model"`
	Position    int    `json:"position"`
	Enabled     bool   `json:"enabled"`
	ChannelName string `json:"channel_name,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
}

type ProxyKeyInfo struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

type ImportSummary struct {
	Channels      int `json:"channels"`
	Keys          int `json:"keys"`
	ProxyKeys     int `json:"proxy_keys"`
	ModelMappings int `json:"model_mappings"`
	Settings      int `json:"settings"`
}

// ─── Helpers ──────────────────────────────────────────────────

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

// ─── Initialization & lifecycle ───────────────────────────────

func New(dbPath, defaultURL string) (*KeyPool, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT UNIQUE NOT NULL, usage_count INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS channels (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL DEFAULT '', prefix TEXT UNIQUE NOT NULL, base_url TEXT NOT NULL, enabled INTEGER DEFAULT 1, is_default INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		return nil, err
	}

	var chCount int
	db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&chCount)
	if chCount == 0 {
		db.Exec(`INSERT INTO channels (name, prefix, base_url, is_default) VALUES ('默认', 'default', ?, 1)`, defaultURL)
	}

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
		"ALTER TABLE channels ADD COLUMN proxy_url TEXT DEFAULT ''",
		"ALTER TABLE model_mappings ADD COLUMN channel_type TEXT DEFAULT 'openai'",
	} {
		db.Exec(col)
	}

	var defaultChID int
	db.QueryRow(`SELECT id FROM channels ORDER BY id LIMIT 1`).Scan(&defaultChID)
	db.Exec(`UPDATE api_keys SET channel_id = ? WHERE channel_id = 0`, defaultChID)

	db.Exec(`UPDATE channels SET prefix = 'default' WHERE prefix = ''`)
	var hasDefault int
	db.QueryRow(`SELECT COUNT(*) FROM channels WHERE is_default = 1`).Scan(&hasDefault)
	if hasDefault == 0 {
		db.Exec(`UPDATE channels SET is_default = 1 WHERE id = (SELECT MIN(id) FROM channels)`)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS proxy_keys (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT UNIQUE NOT NULL, note TEXT DEFAULT '', enabled INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		return nil, err
	}

	// Model mappings migration
	var hasOldMappingTable, hasOldChannelID bool
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='model_mappings'`).Scan(&hasOldMappingTable)
	if hasOldMappingTable {
		db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('model_mappings') WHERE name='channel_id'`).Scan(&hasOldChannelID)
	}

	if hasOldMappingTable && hasOldChannelID {
		type oldMapping struct {
			Name, TargetModel, Note string
			ChannelID               int
			Enabled                 bool
		}
		var oldMappings []oldMapping
		rows, qerr := db.Query(`SELECT name, channel_id, target_model, COALESCE(note,''), enabled FROM model_mappings`)
		if qerr == nil {
			for rows.Next() {
				var om oldMapping
				if rows.Scan(&om.Name, &om.ChannelID, &om.TargetModel, &om.Note, &om.Enabled) == nil {
					oldMappings = append(oldMappings, om)
				}
			}
			rows.Close()
		}
		db.Exec(`DROP TABLE IF EXISTS model_mappings`)
		db.Exec(`DROP TABLE IF EXISTS mapping_targets`)

		if _, err = db.Exec(`CREATE TABLE model_mappings (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, channel_type TEXT NOT NULL DEFAULT 'openai', strategy TEXT NOT NULL DEFAULT 'round-robin', note TEXT DEFAULT '', enabled INTEGER DEFAULT 1, last_target_id INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return nil, err
		}
		if _, err = db.Exec(`CREATE TABLE mapping_targets (id INTEGER PRIMARY KEY AUTOINCREMENT, mapping_id INTEGER NOT NULL, channel_id INTEGER NOT NULL, target_model TEXT NOT NULL DEFAULT '', position INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY (mapping_id) REFERENCES model_mappings(id) ON DELETE CASCADE)`); err != nil {
			return nil, err
		}
		for _, om := range oldMappings {
			res, merr := db.Exec(`INSERT INTO model_mappings (name, strategy, note, enabled) VALUES (?, 'round-robin', ?, ?)`, om.Name, om.Note, boolToInt(om.Enabled))
			if merr != nil {
				continue
			}
			mid, _ := res.LastInsertId()
			db.Exec(`INSERT INTO mapping_targets (mapping_id, channel_id, target_model, position, enabled) VALUES (?, ?, ?, 0, ?)`, mid, om.ChannelID, om.TargetModel, boolToInt(om.Enabled))
		}
	} else if !hasOldMappingTable {
		if _, err = db.Exec(`CREATE TABLE model_mappings (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, channel_type TEXT NOT NULL DEFAULT 'openai', strategy TEXT NOT NULL DEFAULT 'round-robin', note TEXT DEFAULT '', enabled INTEGER DEFAULT 1, last_target_id INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return nil, err
		}
		if _, err = db.Exec(`CREATE TABLE mapping_targets (id INTEGER PRIMARY KEY AUTOINCREMENT, mapping_id INTEGER NOT NULL, channel_id INTEGER NOT NULL, target_model TEXT NOT NULL DEFAULT '', position INTEGER DEFAULT 0, enabled INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY (mapping_id) REFERENCES model_mappings(id) ON DELETE CASCADE)`); err != nil {
			return nil, err
		}
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS request_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, method TEXT, path TEXT, status_code INTEGER, latency_ms INTEGER, proxy_key TEXT, upstream_key TEXT, model TEXT, prompt_tokens INTEGER DEFAULT 0, completion_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0, error TEXT DEFAULT '', request_body TEXT DEFAULT '', response_body TEXT DEFAULT '')`)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS daily_usage (date TEXT PRIMARY KEY, calls INTEGER DEFAULT 0, prompt_tokens INTEGER DEFAULT 0, completion_tokens INTEGER DEFAULT 0, total_tokens INTEGER DEFAULT 0)`)
	if err != nil {
		return nil, err
	}

	var dailyRows int
	db.QueryRow(`SELECT COUNT(*) FROM daily_usage`).Scan(&dailyRows)
	if dailyRows == 0 {
		db.Exec(`INSERT INTO daily_usage (date, calls, prompt_tokens, completion_tokens, total_tokens) SELECT date(created_at, 'localtime'), COUNT(*), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0) FROM request_logs WHERE path LIKE '%chat/completions%' GROUP BY date(created_at, 'localtime')`)
	}

	return &KeyPool{db: db}, nil
}

func (p *KeyPool) Close() error {
	return p.db.Close()
}

// ─── Backup / Import ──────────────────────────────────────────

func (p *KeyPool) ExportBackup() (*BackupData, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	backup := &BackupData{
		Version:    1,
		ExportedAt: time.Now().Format(time.RFC3339),
		Settings:   make(map[string]string),
	}

	var err error
	backup.Channels, err = p.queryChannels()
	if err != nil {
		return nil, err
	}
	backup.Keys, err = p.queryKeys(0)
	if err != nil {
		return nil, err
	}
	backup.ProxyKeys, err = p.queryProxyKeys()
	if err != nil {
		return nil, err
	}
	backup.ModelMappings, err = p.queryModelMappings()
	if err != nil {
		return nil, err
	}

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
		if keyMode == "" || (keyMode != "round-robin" && keyMode != "failover") {
			keyMode = "round-robin"
		}
		channelType := channel.ChannelType
		if channelType == "" {
			channelType = "openai"
		}
		isDefault := channel.IsDefault && !defaultImported
		if isDefault {
			tx.Exec(`UPDATE channels SET is_default = 0`)
			defaultImported = true
		}
		amJSON, _ := json.Marshal(channel.AllowedModels)
		if _, err := tx.Exec(`INSERT INTO channels (name, prefix, base_url, website_url, channel_type, proxy_url, enabled, is_default, pinned_key, key_mode, allowed_models, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(prefix) DO UPDATE SET name=excluded.name, base_url=excluded.base_url, website_url=excluded.website_url, channel_type=excluded.channel_type, proxy_url=excluded.proxy_url, enabled=excluded.enabled, is_default=excluded.is_default, pinned_key=excluded.pinned_key, key_mode=excluded.key_mode, allowed_models=excluded.allowed_models`,
			channel.Name, prefix, channel.BaseURL, channel.WebsiteURL, channelType, channel.ProxyURL, boolToInt(channel.Enabled), boolToInt(isDefault), channel.PinnedKey, keyMode, string(amJSON), defaultCreatedAt(channel.CreatedAt)); err != nil {
			return nil, err
		}
		var newID int
		tx.QueryRow(`SELECT id FROM channels WHERE prefix = ?`, prefix).Scan(&newID)
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
		cid := channelIDMap[key.ChannelID]
		if cid == 0 {
			cid = defaultChannelID
		}
		if _, err := tx.Exec(`INSERT INTO api_keys (key, note, channel_id, default_model, usage_count, prompt_tokens, completion_tokens, total_tokens, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET note=excluded.note, channel_id=excluded.channel_id, default_model=excluded.default_model, usage_count=excluded.usage_count, prompt_tokens=excluded.prompt_tokens, completion_tokens=excluded.completion_tokens, total_tokens=excluded.total_tokens, enabled=excluded.enabled`,
			apiKey, key.Note, cid, key.DefaultModel, key.UsageCount, key.PromptTokens, key.CompletionTokens, key.TotalTokens, boolToInt(key.Enabled), defaultCreatedAt(key.CreatedAt)); err != nil {
			return nil, err
		}
		summary.Keys++
	}

	for _, pk := range backup.ProxyKeys {
		k := strings.TrimSpace(pk.Key)
		if k == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO proxy_keys (key, note, enabled, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET note=excluded.note, enabled=excluded.enabled`,
			k, pk.Note, boolToInt(pk.Enabled), defaultCreatedAt(pk.CreatedAt)); err != nil {
			return nil, err
		}
		summary.ProxyKeys++
	}

	for _, mm := range backup.ModelMappings {
		name := strings.TrimSpace(mm.Name)
		if name == "" {
			continue
		}
		strategy := mm.Strategy
		if strategy == "" || (strategy != "round-robin" && strategy != "failover") {
			strategy = "round-robin"
		}
		ct := mm.ChannelType
		if ct == "" {
			ct = "openai"
		}
		res, err := tx.Exec(`INSERT INTO model_mappings (name, channel_type, strategy, note, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET channel_type=excluded.channel_type, strategy=excluded.strategy, note=excluded.note, enabled=excluded.enabled`,
			name, ct, strategy, mm.Note, boolToInt(mm.Enabled), defaultCreatedAt(mm.CreatedAt))
		if err != nil {
			return nil, err
		}
		mappingID, _ := res.LastInsertId()
		targets := mm.Targets
		if len(targets) == 0 && mm.ChannelID > 0 {
			targets = []ModelMappingTarget{{ChannelID: mm.ChannelID, TargetModel: mm.TargetModel}}
		}
		tx.Exec(`DELETE FROM mapping_targets WHERE mapping_id = ?`, mappingID)
		for i, t := range targets {
			tch := channelIDMap[t.ChannelID]
			if tch == 0 {
				tch = defaultChannelID
			}
			pos := t.Position
			if pos == 0 {
				pos = i
			}
			tx.Exec(`INSERT INTO mapping_targets (mapping_id, channel_id, target_model, position, enabled) VALUES (?, ?, ?, ?, ?)`,
				mappingID, tch, t.TargetModel, pos, boolToInt(t.Enabled))
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
	p.invalidateProxyKeyCache()
	p.invalidateChannelCache()
	return summary, nil
}
