package keypool

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// ============ Channels ============

// loadChannels builds the channel lookup cache from DB. Must be called under write lock.
func (p *KeyPool) loadChannels() {
	p.channelCache = make(map[string]*ChannelInfo)
	p.defaultChannelCache = nil
	rows, err := p.db.Query(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), COALESCE(proxy_url,''), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels ORDER BY is_default DESC, id`)
	if err != nil {
		p.channelCacheLoaded = true
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c ChannelInfo
		var amStr string
		if rows.Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.ProxyURL, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt) != nil {
			continue
		}
		json.Unmarshal([]byte(amStr), &c.AllowedModels)
		if c.Enabled {
			p.channelCache[c.Prefix] = &c
			if c.IsDefault && p.defaultChannelCache == nil {
				cc := c
				p.defaultChannelCache = &cc
			}
		}
	}
	p.channelCacheLoaded = true
}

// invalidateChannelCache clears the channel lookup cache.
func (p *KeyPool) invalidateChannelCache() {
	p.channelCacheLoaded = false
}

func (p *KeyPool) GetAllChannels() ([]ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.queryChannels()
}

func (p *KeyPool) queryChannels() ([]ChannelInfo, error) {
	rows, err := p.db.Query(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), COALESCE(proxy_url,''), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []ChannelInfo
	for rows.Next() {
		var c ChannelInfo
		var amStr string
		if err := rows.Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.ProxyURL, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(amStr), &c.AllowedModels)
		channels = append(channels, c)
	}
	return channels, nil
}

func (p *KeyPool) GetChannelByPrefix(prefix string) (*ChannelInfo, error) {
	p.mu.RLock()
	if p.channelCacheLoaded {
		if c, ok := p.channelCache[prefix]; ok {
			p.mu.RUnlock()
			return c, nil
		}
		p.mu.RUnlock()
		return nil, sql.ErrNoRows
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.channelCacheLoaded {
		p.loadChannels()
	}
	if c, ok := p.channelCache[prefix]; ok {
		return c, nil
	}
	return nil, sql.ErrNoRows
}

func (p *KeyPool) GetDefaultChannel() (*ChannelInfo, error) {
	p.mu.RLock()
	if p.channelCacheLoaded {
		c := p.defaultChannelCache
		p.mu.RUnlock()
		if c != nil {
			return c, nil
		}
		return nil, sql.ErrNoRows
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.channelCacheLoaded {
		p.loadChannels()
	}
	if p.defaultChannelCache != nil {
		return p.defaultChannelCache, nil
	}
	return nil, sql.ErrNoRows
}

func (p *KeyPool) GetChannelByID(id int) (*ChannelInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var c ChannelInfo
	var amStr string
	err := p.db.QueryRow(`SELECT id, name, prefix, base_url, COALESCE(website_url,''), COALESCE(channel_type,'openai'), COALESCE(proxy_url,''), enabled, is_default, COALESCE(pinned_key,''), COALESCE(key_mode,'round-robin'), COALESCE(allowed_models,'[]'), created_at FROM channels WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Prefix, &c.BaseURL, &c.WebsiteURL, &c.ChannelType, &c.ProxyURL, &c.Enabled, &c.IsDefault, &c.PinnedKey, &c.KeyMode, &amStr, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(amStr), &c.AllowedModels)
	return &c, nil
}

func (p *KeyPool) AddChannel(name, prefix, baseURL, websiteURL, channelType, proxyURL string, allowedModels []string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return 0, fmt.Errorf("prefix is required")
	}
	if channelType == "" {
		channelType = "openai"
	}
	amJSON, _ := json.Marshal(allowedModels)
	res, err := p.db.Exec(`INSERT INTO channels (name, prefix, base_url, website_url, channel_type, proxy_url, allowed_models) VALUES (?, ?, ?, ?, ?, ?, ?)`, name, prefix, baseURL, websiteURL, channelType, strings.TrimSpace(proxyURL), string(amJSON))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	p.invalidateChannelCache()
	return int(id), nil
}

func (p *KeyPool) SetDefaultChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`UPDATE channels SET is_default = 0`)
	_, err := p.db.Exec(`UPDATE channels SET is_default = 1 WHERE id = ?`, id)
	p.invalidateChannelCache()
	return err
}

func (p *KeyPool) UpdateChannel(id int, name, prefix, baseURL, websiteURL, channelType, proxyURL string, allowedModels []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	if channelType == "" {
		channelType = "openai"
	}
	amJSON, _ := json.Marshal(allowedModels)
	_, err := p.db.Exec(`UPDATE channels SET name = ?, prefix = ?, base_url = ?, website_url = ?, channel_type = ?, proxy_url = ?, allowed_models = ? WHERE id = ?`, name, prefix, baseURL, websiteURL, channelType, strings.TrimSpace(proxyURL), string(amJSON), id)
	p.invalidateChannelCache()
	return err
}

func (p *KeyPool) RemoveChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	p.invalidateChannelCache()
	return err
}

func (p *KeyPool) EnableChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET enabled = 1 WHERE id = ?`, id)
	p.invalidateChannelCache()
	return err
}

func (p *KeyPool) DisableChannel(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET enabled = 0 WHERE id = ?`, id)
	p.invalidateChannelCache()
	return err
}

// SetKeyMode sets the key selection mode for a channel.
func (p *KeyPool) SetKeyMode(channelID int, mode string) error {
	if mode != "round-robin" && mode != "failover" {
		return fmt.Errorf("invalid key mode: %s", mode)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET key_mode = ? WHERE id = ?`, mode, channelID)
	p.invalidateChannelCache()
	return err
}

// PinKey sets a key as the preferred key for a channel.
func (p *KeyPool) PinKey(channelID int, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET pinned_key = ? WHERE id = ?`, key, channelID)
	p.invalidateChannelCache()
	return err
}

// UnpinKey removes the pinned key for a channel.
func (p *KeyPool) UnpinKey(channelID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE channels SET pinned_key = '' WHERE id = ?`, channelID)
	p.invalidateChannelCache()
	return err
}

// RotateFailoverKey disables the given key and clears the failover_key for the channel.
// Called when a key fails in failover mode, forcing the next request to pick a new key.
func (p *KeyPool) RotateFailoverKey(channelID int, failedKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`UPDATE api_keys SET enabled = 0 WHERE key = ?`, failedKey)
	p.db.Exec(`UPDATE channels SET failover_key = '' WHERE id = ?`, channelID)
	p.invalidateChannelCache()
}
