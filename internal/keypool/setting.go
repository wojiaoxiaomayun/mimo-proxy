package keypool

// ============ Settings ============

// GetSetting returns the value for the given setting key, or defaultValue if
// the key is missing or the stored value is empty. Silently ignores errors
// (e.g. table not yet created).
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
