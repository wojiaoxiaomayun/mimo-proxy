package keypool

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

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
