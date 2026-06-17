package keypool

import "time"

// Application defaults — can be overridden via CLI flags.
var (
	DefaultPort      = 10081
	DefaultTargetURL = "https://api.xiaomimimo.com/v1/chat/completions"
	DefaultDBPath    = "keys.db"
)

// HTTP server timeouts.
const (
	ServerReadTimeout  = 30 * time.Second
	ServerWriteTimeout = 5 * time.Minute
	ServerIdleTimeout  = 120 * time.Second
)

// Connection pool settings (per proxy URL).
const (
	TransportMaxIdleConns        = 100
	TransportMaxIdleConnsPerHost = 20
	TransportIdleConnTimeout     = 90 * time.Second
)

// Key health check thresholds.
const (
	KeyFailThreshold   = 3 // consecutive failures before auto-disable
	ModelsCacheTTL     = 1 * time.Hour
	ModelsFetchTimeout = 15 * time.Second
	LogCleanupInterval = 5 * time.Minute
)
