package keypool

import (
	"path/filepath"
	"testing"
)

// newTestPool creates a KeyPool with a default channel for testing.
func newTestPool(t *testing.T) *KeyPool {
	t.Helper()
	pool, err := New(filepath.Join(t.TempDir(), "mimo.db"), "https://default.example.com/v1/chat/completions")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func defaultChannel(t *testing.T, pool *KeyPool) *ChannelInfo {
	t.Helper()
	ch, err := pool.GetDefaultChannel()
	if err != nil {
		t.Fatalf("GetDefaultChannel() error = %v", err)
	}
	return ch
}

// --------------- Add + Get ---------------

func TestAddModelMapping(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	if err := pool.AddModelMapping("gpt4", ch.ID, "gpt-4-turbo", "alias for gpt4"); err != nil {
		t.Fatalf("AddModelMapping() error = %v", err)
	}

	mappings, err := pool.GetAllModelMappings()
	if err != nil {
		t.Fatalf("GetAllModelMappings() error = %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	m := mappings[0]
	if m.Name != "gpt4" || m.ChannelID != ch.ID || m.TargetModel != "gpt-4-turbo" || m.Note != "alias for gpt4" {
		t.Fatalf("mapping mismatch: %+v", m)
	}
	if !m.Enabled {
		t.Fatal("new mapping should be enabled by default")
	}
	if m.ChannelName == "" {
		t.Fatal("expected ChannelName to be populated via join")
	}
}

func TestAddModelMappingDuplicateName(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	if err := pool.AddModelMapping("dup", ch.ID, "model-a", ""); err != nil {
		t.Fatalf("first AddModelMapping() error = %v", err)
	}
	if err := pool.AddModelMapping("dup", ch.ID, "model-b", ""); err == nil {
		t.Fatal("expected error when adding duplicate mapping name")
	}
}

func TestAddModelMappingToNonexistentChannel(t *testing.T) {
	pool := newTestPool(t)

	// SQLite does not enforce foreign keys by default, but the mapping should still be added.
	err := pool.AddModelMapping("orphan", 9999, "some-model", "")
	if err != nil {
		t.Fatalf("AddModelMapping() with nonexistent channel: unexpected error = %v", err)
	}
}

func TestAddMultipleMappings(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	names := []string{"alpha", "beta", "gamma"}
	for _, n := range names {
		if err := pool.AddModelMapping(n, ch.ID, n+"-model", ""); err != nil {
			t.Fatalf("AddModelMapping(%q) error = %v", n, err)
		}
	}

	mappings, err := pool.GetAllModelMappings()
	if err != nil {
		t.Fatalf("GetAllModelMappings() error = %v", err)
	}
	if len(mappings) != len(names) {
		t.Fatalf("expected %d mappings, got %d", len(names), len(mappings))
	}
}

// --------------- Update ---------------

func TestUpdateModelMapping(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	if err := pool.AddModelMapping("old-name", ch.ID, "old-target", "old note"); err != nil {
		t.Fatalf("AddModelMapping() error = %v", err)
	}
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	if err := pool.UpdateModelMapping(id, "new-name", ch.ID, "new-target", "new note"); err != nil {
		t.Fatalf("UpdateModelMapping() error = %v", err)
	}

	mappings, _ = pool.GetAllModelMappings()
	m := mappings[0]
	if m.Name != "new-name" || m.TargetModel != "new-target" || m.Note != "new note" {
		t.Fatalf("mapping not updated: %+v", m)
	}
}

func TestUpdateModelMappingNonexistent(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Updating a nonexistent ID should not error (0 rows affected).
	if err := pool.UpdateModelMapping(9999, "ghost", ch.ID, "ghost-model", ""); err != nil {
		t.Fatalf("UpdateModelMapping() nonexistent ID: unexpected error = %v", err)
	}
}

func TestUpdateModelMappingChangeChannel(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Add a second channel
	_, err := pool.AddChannel("second", "sec", "https://sec.example.com/v1/chat/completions", "https://sec.example.com", "openai", nil)
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, err := pool.GetChannelByPrefix("sec")
	if err != nil {
		t.Fatalf("GetChannelByPrefix() error = %v", err)
	}

	if err := pool.AddModelMapping("movable", ch.ID, "model-x", ""); err != nil {
		t.Fatalf("AddModelMapping() error = %v", err)
	}
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	if err := pool.UpdateModelMapping(id, "movable", ch2.ID, "model-x", "moved"); err != nil {
		t.Fatalf("UpdateModelMapping() error = %v", err)
	}

	mappings, _ = pool.GetAllModelMappings()
	if mappings[0].ChannelID != ch2.ID {
		t.Fatalf("expected ChannelID %d, got %d", ch2.ID, mappings[0].ChannelID)
	}
	if mappings[0].ChannelName != "second" {
		t.Fatalf("expected ChannelName 'second', got %q", mappings[0].ChannelName)
	}
}

// --------------- Remove ---------------

func TestRemoveModelMapping(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	pool.AddModelMapping("to-delete", ch.ID, "del-model", "")
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	if err := pool.RemoveModelMapping(id); err != nil {
		t.Fatalf("RemoveModelMapping() error = %v", err)
	}

	mappings, _ = pool.GetAllModelMappings()
	if len(mappings) != 0 {
		t.Fatalf("expected 0 mappings after delete, got %d", len(mappings))
	}
}

func TestRemoveModelMappingNonexistent(t *testing.T) {
	pool := newTestPool(t)

	if err := pool.RemoveModelMapping(9999); err != nil {
		t.Fatalf("RemoveModelMapping() nonexistent: unexpected error = %v", err)
	}
}

// --------------- Resolve ---------------

func TestResolveModelMapping(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	pool.AddModelMapping("my-gpt4", ch.ID, "gpt-4-turbo", "")

	gotChID, gotModel, ok := pool.ResolveModelMapping("my-gpt4")
	if !ok {
		t.Fatal("ResolveModelMapping() returned false for existing mapping")
	}
	if gotChID != ch.ID {
		t.Fatalf("ResolveModelMapping() channelID = %d, want %d", gotChID, ch.ID)
	}
	if gotModel != "gpt-4-turbo" {
		t.Fatalf("ResolveModelMapping() targetModel = %q, want %q", gotModel, "gpt-4-turbo")
	}
}

func TestResolveModelMappingNotFound(t *testing.T) {
	pool := newTestPool(t)

	_, _, ok := pool.ResolveModelMapping("nonexistent")
	if ok {
		t.Fatal("ResolveModelMapping() should return false for nonexistent model")
	}
}

func TestResolveModelMappingDisabled(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	pool.AddModelMapping("disabled-alias", ch.ID, "real-model", "")
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	// Disable the mapping directly in DB
	pool.mu.Lock()
	pool.db.Exec(`UPDATE model_mappings SET enabled = 0 WHERE id = ?`, id)
	pool.mu.Unlock()

	_, _, ok := pool.ResolveModelMapping("disabled-alias")
	if ok {
		t.Fatal("ResolveModelMapping() should return false for disabled mapping")
	}
}

func TestResolveModelMappingEmptyModel(t *testing.T) {
	pool := newTestPool(t)

	_, _, ok := pool.ResolveModelMapping("")
	if ok {
		t.Fatal("ResolveModelMapping() should return false for empty model name")
	}
}

func TestResolveModelMappingAfterDelete(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	pool.AddModelMapping("temp", ch.ID, "temp-model", "")
	mappings, _ := pool.GetAllModelMappings()
	pool.RemoveModelMapping(mappings[0].ID)

	_, _, ok := pool.ResolveModelMapping("temp")
	if ok {
		t.Fatal("ResolveModelMapping() should return false after mapping is deleted")
	}
}

// --------------- Resolve with different channels ---------------

func TestResolveModelMappingDifferentChannels(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Add a second channel
	if _, err := pool.AddChannel("anthropic-ch", "ant", "https://api.anthropic.com/v1/messages", "https://anthropic.com", "anthropic", nil); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, _ := pool.GetChannelByPrefix("ant")

	pool.AddModelMapping("gpt4-alias", ch.ID, "gpt-4-turbo", "")
	pool.AddModelMapping("claude-alias", ch2.ID, "claude-sonnet-4-20250514", "")

	// Resolve gpt4 alias → openai channel
	gotCh, gotModel, ok := pool.ResolveModelMapping("gpt4-alias")
	if !ok || gotCh != ch.ID || gotModel != "gpt-4-turbo" {
		t.Fatalf("gpt4-alias: chID=%d model=%q ok=%v, want chID=%d model=%q", gotCh, gotModel, ok, ch.ID, "gpt-4-turbo")
	}

	// Resolve claude alias → anthropic channel
	gotCh, gotModel, ok = pool.ResolveModelMapping("claude-alias")
	if !ok || gotCh != ch2.ID || gotModel != "claude-sonnet-4-20250514" {
		t.Fatalf("claude-alias: chID=%d model=%q ok=%v, want chID=%d model=%q", gotCh, gotModel, ok, ch2.ID, "claude-sonnet-4-20250514")
	}
}

// --------------- Edge cases ---------------

func TestModelMappingEmptyTargetModel(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Target model can be empty string
	if err := pool.AddModelMapping("empty-target", ch.ID, "", "no target"); err != nil {
		t.Fatalf("AddModelMapping() with empty target: error = %v", err)
	}

	mappings, _ := pool.GetAllModelMappings()
	if mappings[0].TargetModel != "" {
		t.Fatalf("expected empty target_model, got %q", mappings[0].TargetModel)
	}

	// Resolve should still work with empty target
	gotCh, gotModel, ok := pool.ResolveModelMapping("empty-target")
	if !ok || gotCh != ch.ID || gotModel != "" {
		t.Fatalf("ResolveModelMapping(empty-target): chID=%d model=%q ok=%v", gotCh, gotModel, ok)
	}
}

func TestModelMappingSpecialCharsInName(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	specialName := "gpt-4o-mini/2024-07-18"
	if err := pool.AddModelMapping(specialName, ch.ID, "gpt-4o-mini", "dated alias"); err != nil {
		t.Fatalf("AddModelMapping() with special chars: error = %v", err)
	}

	_, gotModel, ok := pool.ResolveModelMapping(specialName)
	if !ok || gotModel != "gpt-4o-mini" {
		t.Fatalf("ResolveModelMapping(%q): model=%q ok=%v", specialName, gotModel, ok)
	}
}

func TestGetAllModelMappingsEmpty(t *testing.T) {
	pool := newTestPool(t)

	mappings, err := pool.GetAllModelMappings()
	if err != nil {
		t.Fatalf("GetAllModelMappings() error = %v", err)
	}
	if len(mappings) != 0 {
		t.Fatalf("expected 0 mappings on fresh pool, got %d", len(mappings))
	}
}

func TestModelMappingIdempotentAdd(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	pool.AddModelMapping("idem", ch.ID, "model-1", "")
	// Second add with same name should error
	err := pool.AddModelMapping("idem", ch.ID, "model-2", "")
	if err == nil {
		t.Fatal("expected error on duplicate AddModelMapping")
	}

	// Original mapping should be unchanged
	mappings, _ := pool.GetAllModelMappings()
	if len(mappings) != 1 || mappings[0].TargetModel != "model-1" {
		t.Fatalf("original mapping changed: %+v", mappings)
	}
}
