package keypool

import (
	"path/filepath"
	"testing"
)

func TestGetStatsIncludesTodayUsage(t *testing.T) {
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer pool.Close()

	channel, err := pool.GetDefaultChannel()
	if err != nil {
		t.Fatalf("GetDefaultChannel() error = %v", err)
	}
	if err := pool.Add("upstream-key", "", channel.ID, ""); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := pool.IncrementUsage("upstream-key", 3, 5, 8); err != nil {
		t.Fatalf("IncrementUsage() error = %v", err)
	}

	totalTokens, totalCalls, todayTokens, todayCalls, enabled, disabled, err := pool.GetStats()
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if totalCalls != 1 || totalTokens != 8 || todayCalls != 1 || todayTokens != 8 {
		t.Fatalf("GetStats() totals = totalCalls:%d totalTokens:%d todayCalls:%d todayTokens:%d", totalCalls, totalTokens, todayCalls, todayTokens)
	}
	if enabled != 1 || disabled != 0 {
		t.Fatalf("GetStats() key counts = enabled:%d disabled:%d", enabled, disabled)
	}
}
