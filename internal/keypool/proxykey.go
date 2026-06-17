package keypool

import (
	"crypto/rand"
	"fmt"
)

// ============ Proxy Keys (sk-xxx) ============

func (p *KeyPool) GenerateProxyKey(note string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := fmt.Sprintf("sk-%x", b)
	_, err := p.db.Exec(`INSERT INTO proxy_keys (key, note) VALUES (?, ?)`, key, note)
	p.invalidateProxyKeyCache()
	return key, err
}

// loadProxyKeys loads enabled proxy keys into the cache. Must be called under write lock.
func (p *KeyPool) loadProxyKeys() {
	cache := make(map[string]bool)
	rows, err := p.db.Query(`SELECT key FROM proxy_keys WHERE enabled = 1`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				cache[key] = true
			}
		}
	}
	p.proxyKeyCache = cache
	p.proxyKeyCacheLoaded = true
}

// invalidateProxyKeyCache clears the proxy key cache.
func (p *KeyPool) invalidateProxyKeyCache() {
	p.proxyKeyCacheLoaded = false
}

func (p *KeyPool) ValidateProxyKey(key string) bool {
	p.mu.RLock()
	if p.proxyKeyCacheLoaded {
		ok := p.proxyKeyCache[key]
		p.mu.RUnlock()
		return ok
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.proxyKeyCacheLoaded {
		p.loadProxyKeys()
	}
	return p.proxyKeyCache[key]
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
	p.invalidateProxyKeyCache()
	return err
}

func (p *KeyPool) EnableProxyKey(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE proxy_keys SET enabled = 1 WHERE key = ?`, key)
	p.invalidateProxyKeyCache()
	return err
}

func (p *KeyPool) DisableProxyKey(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE proxy_keys SET enabled = 0 WHERE key = ?`, key)
	p.invalidateProxyKeyCache()
	return err
}
