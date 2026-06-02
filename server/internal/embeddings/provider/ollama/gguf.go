package ollama

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// GGUFInputs bundles the env/config values needed to locate (or
// download) the GGUF weights for the llama-server child. Service
// extracts them from *config.Config so the ollama package stays free
// of the config dependency.
type GGUFInputs struct {
	GGUFPath      string // CIX_GGUF_PATH absolute override
	Model         string // HF repo id ("owner/repo") or absolute path
	CacheDir      string // base dir under which downloaded GGUFs live
	BootstrapPath string // CIX_BOOTSTRAP_GGUF_PATH one-shot import source
}

// ResolveGGUFPath walks the precedence chain:
//  1. in.GGUFPath (absolute path env override, validated by Stat).
//  2. in.Model as absolute path — when the dashboard's "Local path"
//     mode wrote a filesystem path through the runtime_settings row.
//  3. Cached file under in.CacheDir/<safe-repo>/*.gguf when in.Model
//     is an HF repo ID.
//  4. in.BootstrapPath one-shot import — copies the file into the
//     cache layout, then behaves like step 3 forever after.
//  5. HuggingFace download into the same cache (only step that
//     actually writes to disk).
func ResolveGGUFPath(ctx context.Context, in GGUFInputs, logger *slog.Logger) (string, error) {
	if in.GGUFPath != "" {
		if _, err := os.Stat(in.GGUFPath); err != nil {
			return "", fmt.Errorf("CIX_GGUF_PATH=%s: %w", in.GGUFPath, err)
		}
		return in.GGUFPath, nil
	}
	if filepath.IsAbs(in.Model) {
		if _, err := os.Stat(in.Model); err != nil {
			return "", fmt.Errorf("embedding model path %s: %w", in.Model, err)
		}
		return in.Model, nil
	}
	if !strings.Contains(in.Model, "/") {
		return "", fmt.Errorf("embedding model %q is neither an absolute path nor an HF repo id (owner/repo)", in.Model)
	}

	if cached := findCachedGGUF(in.CacheDir, in.Model); cached != "" {
		logger.Info("using cached gguf", "path", cached)
		return cached, nil
	}

	// CIX_BOOTSTRAP_GGUF_PATH — one-time import. Idempotent across
	// boots: subsequent boots find the imported file via findCachedGGUF
	// above and skip this branch entirely.
	if in.BootstrapPath != "" {
		imported, err := importBootstrapGGUF(in.CacheDir, in.Model, in.BootstrapPath, logger)
		if err != nil {
			logger.Warn("bootstrap gguf import failed; falling through to HF download",
				"src", in.BootstrapPath, "err", err)
		} else if imported != "" {
			return imported, nil
		}
	}

	return DownloadGGUF(ctx, in.Model, in.CacheDir, logger)
}

// importBootstrapGGUF copies srcPath into <cacheDir>/<safe_repo>/<basename>
// atomically (write to .partial, fsync, rename). Returns the final path
// on success, "" if the source is missing (caller falls through to HF
// download), or an error for IO problems we should surface to the operator.
//
// safe_repo derived from the HF repo id (`owner/repo` → `owner__repo`)
// to match DownloadGGUF's layout exactly — so subsequent boots' cache
// scan finds the imported file under the same name HF would have used.
func importBootstrapGGUF(cacheDir, repo, srcPath string, logger *slog.Logger) (string, error) {
	if cacheDir == "" || repo == "" {
		return "", nil
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat bootstrap gguf %s: %w", srcPath, err)
	}
	if srcInfo.IsDir() {
		return "", fmt.Errorf("bootstrap gguf %s is a directory, expected file", srcPath)
	}

	safeRepo := strings.ReplaceAll(repo, "/", "__")
	targetDir := filepath.Join(cacheDir, safeRepo)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache dir: %w", err)
	}
	finalPath := filepath.Join(targetDir, filepath.Base(srcPath))

	if _, err := os.Stat(finalPath); err == nil {
		return finalPath, nil
	}

	logger.Info("importing bootstrap gguf into cache",
		"src", srcPath, "dst", finalPath, "size", srcInfo.Size())

	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open bootstrap gguf: %w", err)
	}
	defer src.Close()

	partial := finalPath + ".partial"
	dst, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create cache target: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(partial)
		return "", fmt.Errorf("copy bootstrap gguf: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		_ = os.Remove(partial)
		return "", fmt.Errorf("fsync bootstrap gguf: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("close bootstrap gguf: %w", err)
	}
	if err := os.Rename(partial, finalPath); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("atomic rename bootstrap gguf: %w", err)
	}
	logger.Info("bootstrap gguf imported", "path", finalPath)
	return finalPath, nil
}

// findCachedGGUF looks for a previously-downloaded .gguf under the
// standard cache layout produced by DownloadGGUF. Returns "" on any
// miss (including IO errors) so the caller proceeds to the download
// path.
func findCachedGGUF(cacheDir, repo string) string {
	safeRepo := strings.ReplaceAll(repo, "/", "__")
	dir := cacheDir + string(os.PathSeparator) + safeRepo
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 5 && strings.EqualFold(name[len(name)-5:], ".gguf") {
			return dir + string(os.PathSeparator) + name
		}
	}
	return ""
}
