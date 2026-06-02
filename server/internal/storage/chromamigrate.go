package storage

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// MigrateFlatChromaToNested moves pre-unification FLAT vector-store dirs
// into the unified NESTED layout. Old ollama-only builds wrote one dir per
// model as a flat sibling of the chroma container:
//
//	<base>_<ModelSafeName>      e.g.  /data/chroma_nomic_embed_text
//
// The unified scheme namespaces by provider identity as nested dirs INSIDE
// the container, one path segment per identity field:
//
//	<base>/<kind>/<model-slug>[/<variant>]   e.g.  /data/chroma/ollama/nomic_embed_text
//
// Because the old build only ever ran ollama, EVERY flat sibling is a
// legacy ollama model dir — there is no prefix-guessing (that ambiguity is
// exactly what the nested layout removes: the provider kind is now its own
// directory level, never glued onto the model slug). We move each
// <base>_<X> → <base>/ollama/<StorageSlug(X)>/.
//
// StorageSlug(ModelSafeName(m)) == StorageSlug(m) (ModelSafeName collapses
// only a subset — '/','-' — of the runes StorageSlug collapses, and '_' is
// preserved), so the destination matches the dir the running server
// resolves for "ollama:<model>" — even for a model whose name normalises to
// a kind-looking slug, e.g. "openai-community/x" → ollama/openai_community_x
// (correct), where the old flat scheme silently orphaned it.
//
// Idempotent: once moved, the dirs live inside <base>/ and no longer match
// the flat sibling pattern, so a re-run is a cheap no-op. The caller is
// responsible for first relocating any legacy Python ChromaDB store that
// occupies <base> itself (vectorstore.DetectLegacyAndBackup), so this can
// safely use <base> as the container.
//
// LEGACY-MIGRATION (remove next release): one-time shim. Drop once every
// deployment has booted on the nested layout.
func MigrateFlatChromaToNested(chromaBase string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if chromaBase == "" {
		return nil
	}
	if err := os.MkdirAll(chromaBase, 0o755); err != nil {
		return fmt.Errorf("create chroma container %s: %w", chromaBase, err)
	}

	parent := filepath.Dir(chromaBase)
	prefix := filepath.Base(chromaBase) + "_" // e.g. "chroma_"

	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing indexed yet
		}
		return fmt.Errorf("read chroma parent %s: %w", parent, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue // skip files (e.g. *.tar backups)
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.Contains(name, ".python-backup.") {
			continue // a backup DetectLegacyAndBackup created — leave it
		}
		suffix := strings.TrimPrefix(name, prefix)
		if suffix == "" {
			continue // exactly the base name + trailing "_"
		}

		src := filepath.Join(parent, name)
		// Every flat sibling is a legacy ollama model dir. Canonicalise the
		// suffix through StorageSlug so it matches the path the server
		// resolves for this identity ("ollama:<model>").
		dst := filepath.Join(chromaBase, provider.KindOllama, provider.StorageSlug(suffix))
		if fileExists(dst) {
			logger.Warn("storage: skipping flat→nested chroma move, target already exists (no clobber)",
				"src", src, "dst", dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create nested parent %s: %w", filepath.Dir(dst), err)
		}
		logger.Info("storage: migrating legacy flat chroma dir to nested layout", "src", src, "dst", dst)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
		}
	}
	return nil
}
