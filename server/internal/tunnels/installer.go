package tunnels

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// cloudflaredVersion pins the cloudflared release the managed installer
// downloads. Pinned (not "latest") for reproducibility — keep it in sync
// with the CLOUDFLARED_VERSION arg in server/Dockerfile*. ngrok is fetched
// from its "v3-stable" channel (ngrok publishes no per-version URLs).
//
// This const is the SECOND half of the pin: bumping only the Dockerfiles
// ships a fresh bundled binary while an operator who clicks "Update" in the
// dashboard still gets the old one. Bump both, and refresh pinnedSHA256.
const cloudflaredVersion = "2026.7.3"

// maxBinaryBytes caps a managed download to guard against a decompression
// bomb / runaway response. Comfortably above the real agents (~35 MB).
const maxBinaryBytes = 300 << 20 // 300 MiB

// pinnedSHA256 maps "<provider>/<version>/<goos>/<goarch>" → expected
// hex SHA-256 of the downloaded artifact (raw binary for cloudflared/linux,
// the .tgz for archived assets). When an entry exists the download is
// verified against it; when absent the installer logs a warning with the
// computed sum so an operator can pin it.
//
// cloudflared is pinned to an exact version, so every platform it supports is
// listed here. Cloudflare publishes no checksum file — these were computed
// from the release assets and must be refreshed together with
// cloudflaredVersion.
//
// ngrok is deliberately ABSENT: it is served from a floating "v3-stable"
// channel with no per-version URL, so any sum pinned here would start
// rejecting legitimate downloads the moment ngrok publishes. Those downloads
// fall through to the warn-and-accept path (HTTPS from the official CDN).
var pinnedSHA256 = map[string]string{
	"cloudflare/2026.7.3/linux/amd64":  "9d71c677db00134c1bd4144b7783486b654ad281b1ea62b4972098d19f770f17",
	"cloudflare/2026.7.3/linux/arm64":  "65259e652a7bea08bf5df603233ab22b8bf3116af8df9f9206209af6a1b955c0",
	"cloudflare/2026.7.3/darwin/amd64": "70d1c8684fa6d14b5843787ec8d1ea8e18b23650e424f4ea43d849a506487c3b",
	"cloudflare/2026.7.3/darwin/arm64": "90c5a4f914d705fd70c135dba6d80b1791d254b08d6d4136301941f88330dd09",
}

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

	// Hash the downloaded artifact (the .tgz for archived assets, the raw
	// binary otherwise) while writing, bounded by maxBinaryBytes.
	hasher := sha256.New()
	src := io.TeeReader(io.LimitReader(resp.Body, maxBinaryBytes), hasher)
	if archived {
		if err := extractFromTarGz(src, member, out); err != nil {
			cleanup()
			return err
		}
		// Drain the rest of the archive so the hash covers the whole .tgz.
		_, _ = io.Copy(io.Discard, src)
	} else {
		if _, err := io.Copy(out, src); err != nil {
			cleanup()
			return fmt.Errorf("write binary: %w", err)
		}
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	key := provider + "/" + assetVersion(provider) + "/" + runtime.GOOS + "/" + runtime.GOARCH
	if want, ok := pinnedSHA256[key]; ok {
		if !strings.EqualFold(want, sum) {
			cleanup()
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", key, sum, want)
		}
		in.logger.Info("tunnel binary checksum verified", "provider", provider, "sha256", sum)
	} else {
		in.logger.Warn("tunnel binary downloaded without checksum verification (HTTPS from official source)",
			"provider", provider, "url", url, "sha256", sum)
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
		// Pinned version (not "latest") for reproducibility.
		base := "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/"
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

// assetVersion is the version identifier used in the pinnedSHA256 key for a
// provider (cloudflared is pinned; ngrok tracks its stable channel).
func assetVersion(provider string) string {
	if provider == ProviderNgrok {
		return "v3-stable"
	}
	return cloudflaredVersion
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
