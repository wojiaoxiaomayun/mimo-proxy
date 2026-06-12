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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "gpt-4-turbo"}}
	if err := pool.AddModelMappingGroup("gpt4", "openai", "round-robin", "alias for gpt4", targets); err != nil {
		t.Fatalf("AddModelMappingGroup() error = %v", err)
	}

	mappings, err := pool.GetAllModelMappings()
	if err != nil {
		t.Fatalf("GetAllModelMappings() error = %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	m := mappings[0]
	if m.Name != "gpt4" || m.Note != "alias for gpt4" {
		t.Fatalf("mapping mismatch: %+v", m)
	}
	if !m.Enabled {
		t.Fatal("new mapping should be enabled by default")
	}
	if m.Strategy != "round-robin" {
		t.Fatalf("expected strategy round-robin, got %q", m.Strategy)
	}
	if len(m.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(m.Targets))
	}
	if m.Targets[0].ChannelID != ch.ID || m.Targets[0].TargetModel != "gpt-4-turbo" {
		t.Fatalf("target mismatch: %+v", m.Targets[0])
	}
	if m.Targets[0].ChannelName == "" {
		t.Fatal("expected ChannelName to be populated via join")
	}
}

func TestAddModelMappingMultiTarget(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Add a second channel
	_, err := pool.AddChannel("second", "sec", "https://sec.example.com/v1/chat/completions", "", "openai", "", nil)
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, _ := pool.GetChannelByPrefix("sec")

	targets := []ModelMappingTarget{
		{ChannelID: ch.ID, TargetModel: "gpt-4-turbo"},
		{ChannelID: ch2.ID, TargetModel: "gpt-4o"},
	}
	if err := pool.AddModelMappingGroup("multi", "openai", "round-robin", "multi-target", targets); err != nil {
		t.Fatalf("AddModelMappingGroup() error = %v", err)
	}

	mappings, _ := pool.GetAllModelMappings()
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if len(mappings[0].Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(mappings[0].Targets))
	}
}

func TestAddModelMappingDuplicateName(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "model-a"}}
	if err := pool.AddModelMappingGroup("dup", "openai", "round-robin", "", targets); err != nil {
		t.Fatalf("first AddModelMappingGroup() error = %v", err)
	}
	targets2 := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "model-b"}}
	if err := pool.AddModelMappingGroup("dup", "openai", "round-robin", "", targets2); err == nil {
		t.Fatal("expected error when adding duplicate mapping name")
	}
}

func TestAddModelMappingToNonexistentChannel(t *testing.T) {
	pool := newTestPool(t)

	targets := []ModelMappingTarget{{ChannelID: 9999, TargetModel: "some-model"}}
	err := pool.AddModelMappingGroup("orphan", "openai", "round-robin", "", targets)
	if err != nil {
		t.Fatalf("AddModelMappingGroup() with nonexistent channel: unexpected error = %v", err)
	}
}

func TestAddMultipleMappings(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	names := []string{"alpha", "beta", "gamma"}
	for _, n := range names {
		targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: n + "-model"}}
		if err := pool.AddModelMappingGroup(n, "openai", "round-robin", "", targets); err != nil {
			t.Fatalf("AddModelMappingGroup(%q) error = %v", n, err)
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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "old-target"}}
	pool.AddModelMappingGroup("old-name", "openai", "round-robin", "old note", targets)
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	newTargets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "new-target"}}
	if err := pool.UpdateModelMappingGroup(id, "new-name", "openai", "failover", "new note", newTargets); err != nil {
		t.Fatalf("UpdateModelMappingGroup() error = %v", err)
	}

	m, err := pool.GetModelMappingByID(id)
	if err != nil {
		t.Fatalf("GetModelMappingByID() error = %v", err)
	}
	if m.Name != "new-name" || m.Note != "new note" || m.Strategy != "failover" {
		t.Fatalf("mapping not updated: %+v", m)
	}
	if len(m.Targets) != 1 || m.Targets[0].TargetModel != "new-target" {
		t.Fatalf("target not updated: %+v", m.Targets)
	}
}

func TestUpdateModelMappingNonexistent(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "ghost-model"}}
	// Updating a nonexistent ID should not error (0 rows affected).
	if err := pool.UpdateModelMappingGroup(9999, "ghost", "openai", "round-robin", "", targets); err != nil {
		t.Fatalf("UpdateModelMappingGroup() nonexistent ID: unexpected error = %v", err)
	}
}

func TestUpdateModelMappingChangeChannel(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Add a second channel
	_, err := pool.AddChannel("second", "sec", "https://sec.example.com/v1/chat/completions", "", "openai", "", nil)
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, err := pool.GetChannelByPrefix("sec")
	if err != nil {
		t.Fatalf("GetChannelByPrefix() error = %v", err)
	}

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "model-x"}}
	pool.AddModelMappingGroup("movable", "openai", "round-robin", "", targets)
	mappings, _ := pool.GetAllModelMappings()
	id := mappings[0].ID

	newTargets := []ModelMappingTarget{{ChannelID: ch2.ID, TargetModel: "model-x"}}
	if err := pool.UpdateModelMappingGroup(id, "movable", "openai", "round-robin", "moved", newTargets); err != nil {
		t.Fatalf("UpdateModelMappingGroup() error = %v", err)
	}

	mappings, _ = pool.GetAllModelMappings()
	if mappings[0].Targets[0].ChannelID != ch2.ID {
		t.Fatalf("expected ChannelID %d, got %d", ch2.ID, mappings[0].Targets[0].ChannelID)
	}
	if mappings[0].Targets[0].ChannelName != "second" {
		t.Fatalf("expected ChannelName 'second', got %q", mappings[0].Targets[0].ChannelName)
	}
}

// --------------- Remove ---------------

func TestRemoveModelMapping(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "del-model"}}
	pool.AddModelMappingGroup("to-delete", "openai", "round-robin", "", targets)
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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "gpt-4-turbo"}}
	pool.AddModelMappingGroup("my-gpt4", "openai", "round-robin", "", targets)

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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "real-model"}}
	pool.AddModelMappingGroup("disabled-alias", "openai", "round-robin", "", targets)
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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "temp-model"}}
	pool.AddModelMappingGroup("temp", "openai", "round-robin", "", targets)
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
	if _, err := pool.AddChannel("anthropic-ch", "ant", "https://api.anthropic.com/v1/messages", "", "anthropic", "", nil); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, _ := pool.GetChannelByPrefix("ant")

	pool.AddModelMappingGroup("gpt4-alias", "openai", "round-robin", "", []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "gpt-4-turbo"}})
	pool.AddModelMappingGroup("claude-alias", "anthropic", "failover", "", []ModelMappingTarget{{ChannelID: ch2.ID, TargetModel: "claude-sonnet-4-20250514"}})

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

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: ""}}
	if err := pool.AddModelMappingGroup("empty-target", "openai", "round-robin", "no target", targets); err != nil {
		t.Fatalf("AddModelMappingGroup() with empty target: error = %v", err)
	}

	mappings, _ := pool.GetAllModelMappings()
	if len(mappings) != 1 || mappings[0].Targets[0].TargetModel != "" {
		t.Fatalf("expected empty target model, got %+v", mappings[0].Targets)
	}
}

func TestModelMappingEmptyName(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "x"}}
	// Empty name should cause a UNIQUE constraint or similar error.
	if err := pool.AddModelMappingGroup("", "openai", "round-robin", "", targets); err == nil {
		// SQLite allows empty string for UNIQUE; verify it can be stored.
		mappings, _ := pool.GetAllModelMappings()
		if len(mappings) != 1 {
			t.Fatal("expected mapping with empty name to be stored")
		}
	}
}

func TestResolveModelMappingNoTargets(t *testing.T) {
	pool := newTestPool(t)

	// Create a mapping group with no targets
	pool.mu.Lock()
	pool.db.Exec(`INSERT INTO model_mappings (name, strategy, note) VALUES (?, 'round-robin', '')`, "no-targets")
	pool.mu.Unlock()

	_, _, ok := pool.ResolveModelMapping("no-targets")
	if ok {
		t.Fatal("ResolveModelMapping() should return false for mapping with no targets")
	}
}

// --------------- Round-robin strategy ---------------

func TestResolveRoundRobin(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	_, err := pool.AddChannel("ch2", "ch2", "https://ch2.example.com/v1/chat/completions", "", "openai", "", nil)
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, _ := pool.GetChannelByPrefix("ch2")

	targets := []ModelMappingTarget{
		{ChannelID: ch.ID, TargetModel: "model-a"},
		{ChannelID: ch2.ID, TargetModel: "model-b"},
	}
	pool.AddModelMappingGroup("rr-test", "openai", "round-robin", "", targets)

	// First call should return first target
	gotCh1, gotModel1, ok1 := pool.ResolveModelMapping("rr-test")
	if !ok1 {
		t.Fatal("first resolve failed")
	}

	// Second call should return second target (round-robin)
	gotCh2, gotModel2, ok2 := pool.ResolveModelMapping("rr-test")
	if !ok2 {
		t.Fatal("second resolve failed")
	}

	// Third call should wrap back to first
	gotCh3, gotModel3, ok3 := pool.ResolveModelMapping("rr-test")
	if !ok3 {
		t.Fatal("third resolve failed")
	}

	// Verify round-robin cycling
	if gotCh1 == gotCh2 {
		t.Fatalf("round-robin should cycle: call1 chID=%d, call2 chID=%d", gotCh1, gotCh2)
	}
	if gotCh1 != gotCh3 {
		t.Fatalf("round-robin should wrap: call1 chID=%d, call3 chID=%d", gotCh1, gotCh3)
	}

	_ = gotModel1
	_ = gotModel2
	_ = gotModel3
}

// --------------- Failover strategy ---------------

func TestResolveFailover(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	_, err := pool.AddChannel("ch2", "ch2", "https://ch2.example.com/v1/chat/completions", "", "openai", "", nil)
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	ch2, _ := pool.GetChannelByPrefix("ch2")

	targets := []ModelMappingTarget{
		{ChannelID: ch.ID, TargetModel: "model-a"},
		{ChannelID: ch2.ID, TargetModel: "model-b"},
	}
	pool.AddModelMappingGroup("fo-test", "openai", "failover", "", targets)

	// All calls should return the same first target (failover)
	for i := 0; i < 3; i++ {
		gotCh, gotModel, ok := pool.ResolveModelMapping("fo-test")
		if !ok {
			t.Fatalf("resolve call %d failed", i)
		}
		if gotCh != ch.ID || gotModel != "model-a" {
			t.Fatalf("failover call %d: chID=%d model=%q, want chID=%d model=%q", i, gotCh, gotModel, ch.ID, "model-a")
		}
	}
}

func TestResolveDefaultStrategy(t *testing.T) {
	pool := newTestPool(t)
	ch := defaultChannel(t, pool)

	// Empty strategy should default to round-robin
	targets := []ModelMappingTarget{{ChannelID: ch.ID, TargetModel: "model-x"}}
	pool.AddModelMappingGroup("default-strategy", "", "", "", targets)

	m, err := pool.GetModelMappingByID(1) // first mapping
	if err != nil {
		t.Fatalf("GetModelMappingByID() error = %v", err)
	}
	if m.Strategy != "round-robin" {
		t.Fatalf("empty strategy should default to round-robin, got %q", m.Strategy)
	}
}
