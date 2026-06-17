package keypool

// ============ Model Mappings ============

func (p *KeyPool) GetAllModelMappings() ([]ModelMapping, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.queryModelMappings()
}

func (p *KeyPool) queryModelMappings() ([]ModelMapping, error) {
	rows, err := p.db.Query(`
		SELECT m.id, m.name, COALESCE(m.channel_type,'openai'), COALESCE(m.strategy,'round-robin'), COALESCE(m.note,''), m.enabled, COALESCE(m.last_target_id,0), m.created_at
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
		if err := rows.Scan(&m.ID, &m.Name, &m.ChannelType, &m.Strategy, &m.Note, &m.Enabled, new(int), &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Targets, _ = p.queryTargetsForMapping(m.ID)
		// Enrich display fields from first enabled target
		for _, t := range m.Targets {
			if t.Enabled {
				m.ChannelID = t.ChannelID
				m.TargetModel = t.TargetModel
				m.ChannelName = t.ChannelName
				break
			}
		}
		mappings = append(mappings, m)
	}
	return mappings, nil
}

func (p *KeyPool) queryTargetsForMapping(mappingID int) ([]ModelMappingTarget, error) {
	rows, err := p.db.Query(`
		SELECT t.id, t.mapping_id, t.channel_id, t.target_model, t.position, t.enabled,
		       COALESCE(c.name,''), COALESCE(c.channel_type,'openai')
		FROM mapping_targets t
		LEFT JOIN channels c ON c.id = t.channel_id
		WHERE t.mapping_id = ?
		ORDER BY t.position, t.id
	`, mappingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []ModelMappingTarget
	for rows.Next() {
		var t ModelMappingTarget
		if err := rows.Scan(&t.ID, &t.MappingID, &t.ChannelID, &t.TargetModel, &t.Position, &t.Enabled, &t.ChannelName, &t.ChannelType); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func (p *KeyPool) GetModelMappingByID(id int) (*ModelMapping, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var m ModelMapping
	err := p.db.QueryRow(`
		SELECT m.id, m.name, COALESCE(m.channel_type,'openai'), COALESCE(m.strategy,'round-robin'), COALESCE(m.note,''), m.enabled, m.created_at
		FROM model_mappings m WHERE m.id = ?
	`, id).Scan(&m.ID, &m.Name, &m.ChannelType, &m.Strategy, &m.Note, &m.Enabled, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	m.Targets, _ = p.queryTargetsForMapping(m.ID)
	return &m, nil
}

// GetModelMappingByName looks up a mapping by name (regardless of enabled state).
// Returns nil if not found.
func (p *KeyPool) GetModelMappingByName(modelName string) *ModelMapping {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var m ModelMapping
	err := p.db.QueryRow(`
		SELECT m.id, m.name, COALESCE(m.channel_type,'openai'), COALESCE(m.strategy,'round-robin'), COALESCE(m.note,''), m.enabled, m.created_at
		FROM model_mappings m WHERE m.name = ?
	`, modelName).Scan(&m.ID, &m.Name, &m.ChannelType, &m.Strategy, &m.Note, &m.Enabled, &m.CreatedAt)
	if err != nil {
		return nil
	}
	m.Targets, _ = p.queryTargetsForMapping(m.ID)
	return &m
}

// ToggleModelMapping toggles the enabled state of a model mapping.
func (p *KeyPool) ToggleModelMapping(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE model_mappings SET enabled = NOT enabled WHERE id = ?`, id)
	return err
}

func (p *KeyPool) AddModelMappingGroup(name, channelType, strategy, note string, targets []ModelMappingTarget) error {
	if channelType == "" {
		channelType = "openai"
	}
	if channelType != "openai" && channelType != "anthropic" {
		channelType = "openai"
	}
	if strategy == "" {
		strategy = "round-robin"
	}
	if strategy != "round-robin" && strategy != "failover" {
		strategy = "round-robin"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	res, err := p.db.Exec(`INSERT INTO model_mappings (name, channel_type, strategy, note) VALUES (?, ?, ?, ?)`, name, channelType, strategy, note)
	if err != nil {
		return err
	}
	mappingID, _ := res.LastInsertId()
	for i, t := range targets {
		pos := t.Position
		if pos == 0 {
			pos = i
		}
		_, terr := p.db.Exec(`INSERT INTO mapping_targets (mapping_id, channel_id, target_model, position, enabled) VALUES (?, ?, ?, ?, 1)`,
			mappingID, t.ChannelID, t.TargetModel, pos)
		if terr != nil {
			return terr
		}
	}
	return nil
}

func (p *KeyPool) UpdateModelMappingGroup(id int, name, channelType, strategy, note string, targets []ModelMappingTarget) error {
	if channelType == "" {
		channelType = "openai"
	}
	if channelType != "openai" && channelType != "anthropic" {
		channelType = "openai"
	}
	if strategy == "" {
		strategy = "round-robin"
	}
	if strategy != "round-robin" && strategy != "failover" {
		strategy = "round-robin"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.db.Exec(`UPDATE model_mappings SET name = ?, channel_type = ?, strategy = ?, note = ? WHERE id = ?`, name, channelType, strategy, note, id)
	if err != nil {
		return err
	}
	// Replace all targets
	p.db.Exec(`DELETE FROM mapping_targets WHERE mapping_id = ?`, id)
	for i, t := range targets {
		pos := t.Position
		if pos == 0 {
			pos = i
		}
		_, terr := p.db.Exec(`INSERT INTO mapping_targets (mapping_id, channel_id, target_model, position, enabled) VALUES (?, ?, ?, ?, ?)`,
			id, t.ChannelID, t.TargetModel, pos, boolToInt(t.Enabled))
		if terr != nil {
			return terr
		}
	}
	return nil
}

func (p *KeyPool) RemoveModelMapping(id int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.db.Exec(`DELETE FROM mapping_targets WHERE mapping_id = ?`, id)
	_, err := p.db.Exec(`DELETE FROM model_mappings WHERE id = ?`, id)
	return err
}

// ResolveModelMapping looks up a model name in model_mappings.
// Returns (channelID, targetModel, true) if found and enabled, or (0, "", false) if not a mapping.
func (p *KeyPool) ResolveModelMapping(modelName string) (int, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if modelName == "" {
		return 0, "", false
	}

	// Get mapping group
	var mappingID int
	var strategy string
	var lastTargetID int
	err := p.db.QueryRow(`SELECT id, COALESCE(strategy,'round-robin'), COALESCE(last_target_id,0) FROM model_mappings WHERE name = ? AND enabled = 1`, modelName).Scan(&mappingID, &strategy, &lastTargetID)
	if err != nil {
		return 0, "", false
	}

	// Get enabled targets ordered by position
	rows, err := p.db.Query(`
		SELECT id, channel_id, target_model FROM mapping_targets
		WHERE mapping_id = ? AND enabled = 1
		ORDER BY position, id
	`, mappingID)
	if err != nil {
		return 0, "", false
	}
	defer rows.Close()

	type targetRow struct {
		id          int
		channelID   int
		targetModel string
	}
	var targets []targetRow
	for rows.Next() {
		var t targetRow
		if rows.Scan(&t.id, &t.channelID, &t.targetModel) == nil {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return 0, "", false
	}

	if strategy == "round-robin" {
		// Find last used index, pick next
		idx := 0
		for i, t := range targets {
			if t.id == lastTargetID {
				idx = (i + 1) % len(targets)
				break
			}
		}
		chosen := targets[idx]
		p.db.Exec(`UPDATE model_mappings SET last_target_id = ? WHERE id = ?`, chosen.id, mappingID)
		return chosen.channelID, chosen.targetModel, true
	}

	// failover: always return first enabled target
	return targets[0].channelID, targets[0].targetModel, true
}
