package tunnels

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BinaryStatus describes a provider's agent binary for the dashboard, so it
// can render install instructions (local) or Install/Update buttons
// (managed).
type BinaryStatus struct {
	Provider  string `json:"provider"`
	Installed bool   `json:"installed"`
	Managed   bool   `json:"managed"` // server can install/update it from the UI
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// binaryName maps a provider to its executable name.
func binaryName(provider string) string {
	if provider == ProviderNgrok {
		return "ngrok"
	}
	return "cloudflared"
}

// Installer downloads provider agent binaries into a writable managed
// directory, without a shell (pure Go: HTTP + tar/gzip). It is constructed
// only when CIX_TUNNEL_BIN_MANAGED=true — typically inside Docker, where the
// bundled binaries live on a read-only image layer and can't be updated in
// place.
type Installer struct {
	dir    string
	logger *slog.Logger
	client *http.Client
}

func NewInstaller(dir string, logger *slog.Logger) *Installer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Installer{
		dir:    dir,
		logger: logger,
		// Binary downloads can be tens of MB; allow a generous ceiling.
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Path returns the managed path for a provider's binary (whether or not it
// exists yet).
func (in *Installer) Path(provider string) string {
	return filepath.Join(in.dir, binaryName(provider))
}

// Installed reports whether the managed binary exists and is executable.
func (in *Installer) Installed(provider string) (bool, string) {
	p := in.Path(provider)
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return false, ""
	}
	return true, p
}

// Install downloads the latest agent binary for the current OS/arch into the
// managed directory, atomically replacing any existing one. Install and
// update share this path: it always fetches the latest stable release.
func (in *Installer) Install(ctx context.Context, provider string) error {
	url, archived, member, err := assetURL(provider, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(in.dir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	in.logger.Info("downloading tunnel binary", "provider", provider, "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := in.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", provider, resp.StatusCode)
	}

	final := in.Path(provider)
	partial := final + ".partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create binary file: %w", err)
	}
	cleanup := func() { _ = out.Close(); _ = os.Remove(partial) }

	if archived {
		if err := extractFromTarGz(resp.Body, member, out); err != nil {
			cleanup()
			return err
		}
	} else {
		if _, err := io.Copy(out, resp.Body); err != nil {
			cleanup()
			return fmt.Errorf("write binary: %w", err)
		}
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fsync binary: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("close binary: %w", err)
	}
	// Atomic rename — safe even if the old binary is currently running
	// (Linux keeps the old inode for the live process; a Restart re-execs
	// the new one).
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("install binary: %w", err)
	}
	in.logger.Info("tunnel binary installed", "provider", provider, "path", final)
	return nil
}

// extractFromTarGz copies the named member of a .tgz stream into out.
func extractFromTarGz(r io.Reader, member string, out io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive did not contain %q", member)
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) == member && hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // size-bounded by client timeout
				return fmt.Errorf("extract %q: %w", member, err)
			}
			return nil
		}
	}
}

// assetURL returns the official download URL for a provider's agent for the
// given OS/arch, whether it's a .tgz archive, and the member name inside.
func assetURL(provider, goos, goarch string) (url string, archived bool, member string, err error) {
	switch provider {
	case ProviderCloudflare:
		const base = "https://github.com/cloudflare/cloudflared/releases/latest/download/"
		switch goos {
		case "linux":
			return base + "cloudflared-linux-" + goarch, false, "", nil
		case "darwin":
			return base + "cloudflared-darwin-" + goarch + ".tgz", true, "cloudflared", nil
		default:
			return "", false, "", fmt.Errorf("managed cloudflared install unsupported on %s", goos)
		}
	case ProviderNgrok:
		// ngrok ships a .tgz for every platform via its equinox CDN.
		return "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable-" + goos + "-" + goarch + ".tgz", true, "ngrok", nil
	default:
		return "", false, "", fmt.Errorf("unknown provider %q", provider)
	}
}

// binaryVersion runs the agent's version subcommand best-effort. Returns ""
// on any failure — it's informational only.
func binaryVersion(ctx context.Context, provider, path string) string {
	var args []string
	switch provider {
	case ProviderNgrok:
		args = []string{"version"}
	default:
		args = []string{"--version"}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
