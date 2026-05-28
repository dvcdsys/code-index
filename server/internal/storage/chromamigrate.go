package storage

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
)

// knownProviderPrefixes are the StorageSlug prefixes a vector-store dir
// gets under the unified scheme (provider.StorageSlug of an ID always
// starts with "<kind>_"). A legacy dir lacking any of these was produced
// by the old model-only naming, which only ever ran the ollama sidecar.
var knownProviderPrefixes = []string{"ollama_", "openai_", "voyage_"}

// PrefixLegacyChromaDirs renames legacy, un-prefixed vector-store
// directories to the unified "ollama_"-prefixed form so existing ollama
// vectors survive the switch to provider-identity namespacing WITHOUT a
// reindex. Heuristic: a dir named "<base>_<X>" where X carries no known
// provider prefix was written by the pre-unification ollama-only build,
// so its true identity is "ollama:<model>" → "<base>_ollama_<slug(X)>".
//
// The legacy suffix X was produced by the old Config.ModelSafeName (which
// only mapped '/' and '-' to '_'), but the running server resolves the
// dir via provider.StorageSlug, which maps EVERY non-[a-z0-9_] rune. For
// model names containing characters the two normalizers treat differently
// (e.g. a '.' or ':' in "nomic-embed-text:v1.5"), a naive "<base>_ollama_"+X
// would NOT equal the dir the server opens, silently orphaning the vectors.
// We therefore re-run the suffix through provider.StorageSlug. This is
// exact for ALL models because StorageSlug(ModelSafeName(m)) ==
// StorageSlug(m): ModelSafeName only collapses a subset ('/','-') of the
// runes StorageSlug collapses, and '_' is preserved by StorageSlug, so the
// two compose to the same canonical form.
//
// chromaBase is cfg.ChromaPersistDir (e.g. ".../chroma"); legacy dirs sit
// next to it as ".../chroma_<slug>". The scan covers ALL such dirs, not
// just the active provider's: the active provider may be voyage (no legacy
// dir to migrate) while the operator's ollama vectors wait under the
// un-prefixed name for a future switch back.
//
// Idempotent: already-prefixed dirs are skipped; a rename whose target
// already exists is skipped with a warning (never clobbers).
//
// LEGACY-MIGRATION (remove next release): one-time prefixing shim for
// pre-unification ollama-only chroma dirs. Once every deployment has
// booted on the unified layout, delete this function and its calls in
// cmd/cix-server/main.go and embeddings.Service (the AttachVectorStore
// migrate hook).
func PrefixLegacyChromaDirs(chromaBase string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if chromaBase == "" {
		return nil
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
			continue // skip files like chroma_*.python-backup.* tarballs
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		if suffix == "" {
			continue // exactly the base dir name + trailing "_": ignore
		}
		if hasKnownPrefix(suffix) {
			continue // already migrated / native unified dir
		}
		src := filepath.Join(parent, name)
		// Canonicalise the suffix through StorageSlug so the renamed dir
		// matches the path the running server resolves for this identity
		// (handles model names with '.'/':' etc.; see func doc).
		dst := filepath.Join(parent, prefix+"ollama_"+provider.StorageSlug(suffix))
		if fileExists(dst) {
			logger.Warn("storage: skipping chroma legacy-prefix rename, target already exists",
				"src", src, "dst", dst)
			continue
		}
		logger.Info("storage: prefixing legacy ollama chroma dir to unified naming",
			"src", src, "dst", dst)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
		}
	}
	return nil
}

func hasKnownPrefix(suffix string) bool {
	for _, p := range knownProviderPrefixes {
		if strings.HasPrefix(suffix, p) {
			return true
		}
	}
	return false
}
