package embeddingscfg

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// openTestService opens an in-memory DB (all migrations applied, including
// migration 12 which adds the embedding_provider columns) and wraps it.
func openTestService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return New(d)
}

// TestGet_FreshDB_NoRow covers the "no provider persisted yet" path: the
// runtime_settings row has no embedding_provider, so Get reports has=false
// and the caller is expected to seed from env.
func TestGet_FreshDB_NoRow(t *testing.T) {
	s := openTestService(t)
	snap, has, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if has {
		t.Fatalf("has = true on a fresh DB, want false (snap=%+v)", snap)
	}
}

// TestSave_Insert_ThenGet covers the INSERT branch (no row yet) and a
// round-trip read of kind + config.
func TestSave_Insert_ThenGet(t *testing.T) {
	s := openTestService(t)
	cfg := json.RawMessage(`{"model":"voyage-code-3","output_dimension":2048}`)
	if err := s.Save(context.Background(), Snapshot{Kind: "voyage", Config: cfg}, "admin-1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snap, has, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !has {
		t.Fatalf("has = false after Save, want true")
	}
	if snap.Kind != "voyage" {
		t.Errorf("Kind = %q, want voyage", snap.Kind)
	}
	if string(snap.Config) != string(cfg) {
		t.Errorf("Config = %q, want %q", snap.Config, cfg)
	}
}

// TestSave_Update_Overwrites covers the UPDATE branch (row already exists):
// a second Save must overwrite the persisted kind + config rather than
// inserting a duplicate or being ignored.
func TestSave_Update_Overwrites(t *testing.T) {
	s := openTestService(t)
	ctx := context.Background()
	if err := s.Save(ctx, Snapshot{Kind: "ollama", Config: json.RawMessage(`{"model":"a"}`)}, ""); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	newCfg := json.RawMessage(`{"model":"text-embedding-3-large","dimensions":256}`)
	if err := s.Save(ctx, Snapshot{Kind: "openai", Config: newCfg}, "admin-2"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	snap, has, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !has || snap.Kind != "openai" {
		t.Fatalf("after overwrite: has=%v kind=%q, want has=true kind=openai", has, snap.Kind)
	}
	if string(snap.Config) != string(newCfg) {
		t.Errorf("Config = %q, want %q", snap.Config, newCfg)
	}
}

// TestSave_EmptyConfig covers a provider persisted with no config blob:
// json.Valid is skipped (len 0) and Get reports has=true with a nil Config.
func TestSave_EmptyConfig(t *testing.T) {
	s := openTestService(t)
	ctx := context.Background()
	if err := s.Save(ctx, Snapshot{Kind: "ollama"}, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snap, has, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !has || snap.Kind != "ollama" {
		t.Fatalf("has=%v kind=%q, want has=true kind=ollama", has, snap.Kind)
	}
	if len(snap.Config) != 0 {
		t.Errorf("Config = %q, want empty", snap.Config)
	}
}

// TestSave_RejectsEmptyKind: a Snapshot with no kind is a programming error
// and must be refused before touching the DB.
func TestSave_RejectsEmptyKind(t *testing.T) {
	s := openTestService(t)
	if err := s.Save(context.Background(), Snapshot{Config: json.RawMessage(`{}`)}, ""); err == nil {
		t.Fatal("Save with empty kind succeeded, want error")
	}
}

// TestSave_RejectsInvalidJSON: a non-empty config that isn't valid JSON must
// be refused so a malformed blob never lands in the DB for boot to choke on.
func TestSave_RejectsInvalidJSON(t *testing.T) {
	s := openTestService(t)
	err := s.Save(context.Background(), Snapshot{Kind: "voyage", Config: json.RawMessage(`{not json`)}, "")
	if err == nil {
		t.Fatal("Save with invalid JSON config succeeded, want error")
	}
}
