package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func TestMigrateFlatChromaToNested(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma") // container; flat legacy dirs are chroma_<X>

	// Legacy flat ollama dir (should move into chroma/ollama/<slug>).
	mkdir(t, base+"_awhiteside_coderankembed_q8_0_gguf")
	// A file that matches the prefix but is not a dir (ignored).
	if err := os.WriteFile(base+"_backup.tar", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if dirExists(base + "_awhiteside_coderankembed_q8_0_gguf") {
		t.Errorf("legacy flat dir should have been moved away")
	}
	if !dirExists(filepath.Join(base, "ollama", "awhiteside_coderankembed_q8_0_gguf")) {
		t.Errorf("expected nested dir chroma/ollama/awhiteside_coderankembed_q8_0_gguf")
	}
	if !fileExists(base + "_backup.tar") {
		t.Errorf("non-dir entry must be ignored, not moved")
	}
}

// TestMigrateFlatChromaToNested_KindLookingModelName is the #8 regression:
// a legacy ollama model whose name normalises to a known-kind-looking slug
// (e.g. "openai-community/x" → "openai_community_x") was SKIPPED by the old
// prefix heuristic and silently orphaned. With the nested layout the
// provider kind is its own path segment, so the dir is correctly moved to
// chroma/ollama/openai_community_x — exactly where the server resolves
// "ollama:openai-community/x".
func TestMigrateFlatChromaToNested_KindLookingModelName(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	mkdir(t, base+"_openai_community_x") // ModelSafeName("openai-community/x")

	if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !dirExists(filepath.Join(base, "ollama", "openai_community_x")) {
		t.Errorf("kind-looking legacy ollama dir must move to chroma/ollama/openai_community_x, not be skipped")
	}
	if dirExists(base + "_openai_community_x") {
		t.Errorf("legacy flat dir should have been moved away")
	}
}

// TestMigrateFlatChromaToNested_StrictNormalizesSpecialChars: the legacy
// suffix was written by ModelSafeName (only '/'->'_' and '-'->'_'), so a
// model like "nomic-embed-text:v1.5" left a dir holding a '.'/':'. The
// running server resolves via StorageSlug (every non-[a-z0-9_] -> '_'), so
// the migration must canonicalise identically or the vectors are orphaned.
func TestMigrateFlatChromaToNested_StrictNormalizesSpecialChars(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	mkdir(t, base+"_nomic_embed_text_v1.5") // '.' survives in the legacy name

	if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !dirExists(filepath.Join(base, "ollama", "nomic_embed_text_v1_5")) {
		t.Errorf("expected strict-normalized nested dir chroma/ollama/nomic_embed_text_v1_5")
	}
	if dirExists(base + "_nomic_embed_text_v1.5") {
		t.Errorf("legacy dir should have been moved away")
	}
}

func TestMigrateFlatChromaToNested_NoClobber(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	// Both the legacy source and its would-be nested target already exist.
	mkdir(t, base+"_model_x")
	mkdir(t, filepath.Join(base, "ollama", "model_x"))
	if err := os.WriteFile(filepath.Join(base+"_model_x", "marker"), []byte("src"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Source left in place (not clobbered into the existing target).
	if !dirExists(base + "_model_x") {
		t.Errorf("source dir must be preserved when target already exists")
	}
	if _, err := os.Stat(filepath.Join(base+"_model_x", "marker")); err != nil {
		t.Errorf("source contents must be intact: %v", err)
	}
}

func TestMigrateFlatChromaToNested_Idempotent(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	mkdir(t, base+"_legacy_model")

	for i := 0; i < 2; i++ {
		if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !dirExists(filepath.Join(base, "ollama", "legacy_model")) {
		t.Errorf("expected nested dir after idempotent runs")
	}
	if dirExists(base + "_legacy_model") {
		t.Errorf("legacy flat dir should be gone after first run")
	}
}

func TestMigrateFlatChromaToNested_IgnoresPythonBackup(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	backup := base + "_model.python-backup.20250101-000000"
	mkdir(t, backup)

	if err := MigrateFlatChromaToNested(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// A python-backup dir must be left exactly where it is.
	if !dirExists(backup) {
		t.Errorf("python-backup dir must not be moved")
	}
	if dirExists(filepath.Join(base, "ollama")) {
		t.Errorf("python-backup dir must not be treated as a legacy ollama namespace")
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
