package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hfProgressChunk controls how often the downloader logs progress. 10 MiB
// matches the plan's "log progress every 10MB via slog.Info".
const hfProgressChunk = 10 * 1024 * 1024

// hfAPITimeout caps the metadata GET; downloads use their own per-response
// body reader without a hard timeout so huge files (600MB+) do not trip it.
const hfAPITimeout = 30 * time.Second

// hfFileEntry is a subset of the HuggingFace models API response — we only
// care about the file listing, so we tolerate unknown fields.
type hfFileEntry struct {
	RFilename string `json:"rfilename"`
}

type hfModelInfo struct {
	Siblings []hfFileEntry `json:"siblings"`
}

// DownloadGGUF pulls the first `.gguf` file from the given public HuggingFace
// repository into cacheDir/<repo-safe>/ and returns the absolute path. If the
// file already exists, it is returned without re-downloading. The download is
// atomic: bytes go to a `.partial` sibling, then os.Rename flips it into place
// so concurrent callers never observe a half-written file.
//
// This function is only called from Service.New when CIX_GGUF_PATH is empty,
// the dev-fallback returned nothing, and the repo cache has no matching file.
func DownloadGGUF(ctx context.Context, repo, cacheDir string, logger *slog.Logger) (string, error) {
	if repo == "" {
		return "", errors.New("hfdownload: empty repo")
	}
	if cacheDir == "" {
		return "", errors.New("hfdownload: empty cacheDir")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Layout the cache like `<cacheDir>/<safe-repo>/<filename>` so multiple
	// models coexist without colliding. "/" is not legal in path segments on
	// any platform we target, so replace with "__".
	safeRepo := strings.ReplaceAll(repo, "/", "__")
	targetDir := filepath.Join(cacheDir, safeRepo)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir cache dir: %w", err)
	}

	// 1. Ask the API which files live in the repo, pick the first .gguf.
	info, err := fetchModelInfo(ctx, repo)
	if err != nil {
		return "", err
	}
	var filename string
	for _, s := range info.Siblings {
		if strings.HasSuffix(strings.ToLower(s.RFilename), ".gguf") {
			filename = s.RFilename
			break
		}
	}
	if filename == "" {
		return "", fmt.Errorf("hfdownload: no .gguf found in repo %s", repo)
	}

	finalPath := filepath.Join(targetDir, filepath.Base(filename))
	if _, err := os.Stat(finalPath); err == nil {
		logger.Info("gguf already cached", "path", finalPath, "repo", repo)
		return finalPath, nil
	}

	// 2. Stream the file to <finalPath>.partial, log every 10 MiB, atomic rename.
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, filename)
	logger.Info("downloading gguf from huggingface", "repo", repo, "file", filename, "url", url)

	if err := streamDownload(ctx, url, finalPath, logger); err != nil {
		return "", err
	}
	logger.Info("gguf download complete", "path", finalPath)
	return finalPath, nil
}

// fetchModelInfo GETs /api/models/<repo>. Public models need no auth.
func fetchModelInfo(ctx context.Context, repo string) (*hfModelInfo, error) {
	apiCtx, cancel := context.WithTimeout(ctx, hfAPITimeout)
	defer cancel()

	url := fmt.Sprintf("https://huggingface.co/api/models/%s", repo)
	req, err := http.NewRequestWithContext(apiCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build hf api request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hf api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hf api %s: status %d", repo, resp.StatusCode)
	}
	var info hfModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode hf api: %w", err)
	}
	return &info, nil
}

// streamDownload performs the actual byte transfer with progress logging and
// atomic rename semantics. A failed transfer leaves the .partial file behind
// so curl-style resume could be added later if needed — Phase 3 does not.
func streamDownload(ctx context.Context, url, finalPath string, logger *slog.Logger) error {
	partialPath := finalPath + ".partial"

	// Use a client without Timeout so that huge models do not time out mid-stream.
	// We still honour ctx for cancellation via the request context.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("gguf download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gguf download: status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(partialPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create partial: %w", err)
	}
	// If anything below fails, make sure we do not leave a partial behind.
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(partialPath)
		}
	}()

	total := resp.ContentLength
	buf := make([]byte, 64*1024)
	var (
		written    int64
		lastLogged int64
	)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write partial: %w", werr)
			}
			written += int64(n)
			if written-lastLogged >= hfProgressChunk {
				if total > 0 {
					logger.Info("gguf download progress",
						"bytes", written,
						"total", total,
						"pct", fmt.Sprintf("%.1f", float64(written)*100/float64(total)),
					)
				} else {
					logger.Info("gguf download progress", "bytes", written)
				}
				lastLogged = written
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read body: %w", readErr)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync partial: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close partial: %w", err)
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return fmt.Errorf("rename partial: %w", err)
	}
	committed = true
	return nil
}
