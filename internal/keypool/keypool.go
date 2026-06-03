package keypool

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type KeyInfo struct {
	ID         int    `json:"id"`
	Key        string `json:"key"`
	UsageCount int    `json:"usage_count"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
}

type KeyPool struct {
	db *sql.DB
	mu sync.RWMutex
}

func New(dbPath string) (*KeyPool, error) {
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

	return &KeyPool{db: db}, nil
}

func (p *KeyPool) GetKey() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var key string
	var enabled bool
	err := p.db.QueryRow(`
		SELECT key, enabled FROM api_keys 
		WHERE enabled = 1 
		ORDER BY usage_count ASC, id ASC 
		LIMIT 1
	`).Scan(&key, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no available API key")
		}
		return "", err
	}
	return key, nil
}

func (p *KeyPool) IncrementUsage(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`
		UPDATE api_keys SET usage_count = usage_count + 1 WHERE key = ?
	`, key)
	return err
}

func (p *KeyPool) Add(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`INSERT OR IGNORE INTO api_keys (key) VALUES (?)`, key)
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

func (p *KeyPool) GetAll() ([]KeyInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	rows, err := p.db.Query(`
		SELECT id, key, usage_count, enabled, created_at FROM api_keys ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.Key, &k.UsageCount, &k.Enabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (p *KeyPool) GetStats() (totalRequests int64, enabledCount, disabledCount int, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	err = p.db.QueryRow(`SELECT COALESCE(SUM(usage_count), 0) FROM api_keys`).Scan(&totalRequests)
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
	keys := strings.Split(keysStr, ",")
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			if err := p.Add(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *KeyPool) Close() error {
	return p.db.Close()
}