package keypool

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
