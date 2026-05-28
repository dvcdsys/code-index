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

func TestPrefixLegacyChromaDirs(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma") // chromaBase; dirs are chroma_<slug>

	// Legacy un-prefixed ollama dir (should be renamed).
	mkdir(t, base+"_awhiteside_coderankembed_q8_0_gguf")
	// Already-prefixed dirs (must be left untouched).
	mkdir(t, base+"_voyage_voyage_code_3_2048_float")
	mkdir(t, base+"_ollama_already")
	// A file that matches the prefix but is not a dir (ignored).
	if err := os.WriteFile(base+"_backup.tar", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PrefixLegacyChromaDirs(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Legacy renamed to ollama-prefixed.
	if dirExists(base + "_awhiteside_coderankembed_q8_0_gguf") {
		t.Errorf("legacy un-prefixed dir should have been renamed away")
	}
	if !dirExists(base + "_ollama_awhiteside_coderankembed_q8_0_gguf") {
		t.Errorf("expected renamed dir chroma_ollama_awhiteside_coderankembed_q8_0_gguf")
	}
	// Already-prefixed untouched.
	if !dirExists(base + "_voyage_voyage_code_3_2048_float") {
		t.Errorf("voyage dir must be left untouched")
	}
	if !dirExists(base + "_ollama_already") {
		t.Errorf("ollama-prefixed dir must be left untouched")
	}
	// File untouched.
	if !fileExists(base + "_backup.tar") {
		t.Errorf("non-dir entry must be ignored, not moved")
	}
}

// TestPrefixLegacyChromaDirs_StrictNormalizesSpecialChars guards Finding 1:
// the legacy suffix was written by ModelSafeName (only '/'->'_' and
// '-'->'_'), so a model like "nomic-embed-text:v1.5" left a dir whose name
// still held a '.'/':'. The running server resolves the dir via
// provider.StorageSlug (every non-[a-z0-9_] -> '_'), so the migration must
// canonicalise the suffix the same way or the vectors are silently orphaned.
func TestPrefixLegacyChromaDirs_StrictNormalizesSpecialChars(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	// Legacy dir as ModelSafeName would have produced for a v1.5 model:
	// the '.' survives in the on-disk name.
	mkdir(t, base+"_nomic_embed_text_v1.5")

	if err := PrefixLegacyChromaDirs(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Must land on the StorageSlug form the server actually opens ('.'->'_').
	if !dirExists(base + "_ollama_nomic_embed_text_v1_5") {
		t.Errorf("expected strict-normalized dir chroma_ollama_nomic_embed_text_v1_5")
	}
	if dirExists(base + "_nomic_embed_text_v1.5") {
		t.Errorf("legacy dir should have been renamed away")
	}
}

func TestPrefixLegacyChromaDirs_NoClobber(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	// Both the legacy source and its would-be target already exist.
	mkdir(t, base+"_model_x")
	mkdir(t, base+"_ollama_model_x")
	if err := os.WriteFile(filepath.Join(base+"_model_x", "marker"), []byte("src"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PrefixLegacyChromaDirs(base, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Source left in place (not clobbered into existing target).
	if !dirExists(base + "_model_x") {
		t.Errorf("source dir must be preserved when target already exists")
	}
	if _, err := os.Stat(filepath.Join(base+"_model_x", "marker")); err != nil {
		t.Errorf("source contents must be intact: %v", err)
	}
}

func TestPrefixLegacyChromaDirs_Idempotent(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "chroma")
	mkdir(t, base+"_legacy_model")

	for i := 0; i < 2; i++ {
		if err := PrefixLegacyChromaDirs(base, quietLogger()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !dirExists(base + "_ollama_legacy_model") {
		t.Errorf("expected ollama-prefixed dir after idempotent runs")
	}
	if dirExists(base + "_legacy_model") {
		t.Errorf("legacy dir should be gone after first run")
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
