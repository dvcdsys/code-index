//go:build darwin || windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/release"
)

// The cix runtime: cix-server, the cix CLI, and the Metal llama-server they
// depend on. It is not inside the .app.
//
// Layout
// ------
//
//	~/.cix/runtime/
//	  0.12.8/     cix-server  cix  llama/…  runtime.json
//	  0.12.7/     the version this one replaced, kept for rollback
//	  current ->  0.12.8
//
// Versioned by the SERVER, from the server/v* tag stream — the same tag and the
// same workflow run that publishes the Docker images. A Mac install on 0.12.8
// and a container on 0.12.8 are the same server. The app is versioned
// separately, on mac/v*, because it changes for different reasons; the two
// update independently and neither waits for the other.
//
// Why outside the bundle
// ----------------------
// The runtime is 90% of the weight and nearly all of the churn. With it inside,
// every server update replaced a 102 MB application through a trampoline that
// had to quit the launcher, move the bundle aside, move the new one in, and
// reopen. Out here, an update is a download and a rename, with the app still
// running.
//
// Why a symlink rather than a fixed directory
// -------------------------------------------
// Renaming a symlink over another is atomic, so there is no window in which
// `current` points at half a runtime — and it is reversible in microseconds,
// which is what makes the automatic rollback below possible without a second
// download. The launchd wrapper execs the `current` path, so a swap needs no
// plist rewrite and no bootout/bootstrap cycle either.
//
// The symlink target is relative (`0.5.0`, not an absolute path) so the tree
// survives being moved with the home directory.

const (
	runtimeLinkName     = "current"
	runtimeManifestName = "runtime.json"

	// The directory inside the archive. Set by mac/scripts/build-runtime.sh and
	// win/scripts/build-runtime.sh; it is a format, not a convention. The asset's
	// suffix is platform-specific (runtimeAssetSuffix, in runtime_<os>.go).
	runtimeDirPrefix = "cix-runtime-"
)

// runtimeInfo is runtime.json, written by build-runtime.sh.
//
// Reading a manifest rather than exec'ing each binary with -v is what lets the
// menu show what is installed without three process spawns per open — and it is
// the only place the llama version is recorded at all. There is no separate
// runtime version: the runtime IS the server, so the directory it lives in is
// named for ServerVersion.
type runtimeInfo struct {
	ServerVersion string `json:"server_version"`
	CLIVersion    string `json:"cli_version"`
	LlamaVersion  string `json:"llama_version"`
	Platform      string `json:"platform"`
	// CUDA is the toolkit generation the bundled llama-server was built against
	// (Windows only; empty on macOS, which uses Metal).
	CUDA string `json:"cuda,omitempty"`
}

func runtimeRoot() (string, error) {
	dir, err := cixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime"), nil
}

// runtimeCurrentDir returns the symlink path itself, deliberately unresolved.
//
// Everything that has to survive a version swap — the launchd wrapper, the
// /usr/local/bin/cix symlink — must point here rather than at a versioned
// directory, or it would pin itself to the version that happened to be current
// when it was written.
func runtimeCurrentDir() (string, error) {
	root, err := runtimeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, runtimeLinkName), nil
}

// runtimeLiveDir is where the launcher itself reads and executes the current
// runtime. It is the same place as runtimeCurrentDir on macOS, where a symlink is
// transparent to every process. On Windows it is the junction's target: see
// runtime_windows.go for why the launcher must not walk through the junction
// itself.
func runtimeLiveDir() (string, error) {
	dir, err := runtimeCurrentDir()
	if err != nil {
		return "", err
	}
	return resolveRuntimeLink(dir), nil
}

// runtimeLiveServerPath is the server binary as the launcher executes it
// (password reset, version probes). The service definition uses the stable
// runtimeServerPath instead, so it survives a runtime swap unchanged.
func runtimeLiveServerPath() (string, error) {
	dir, err := runtimeLiveDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cix-server"+exeSuffix), nil
}

func runtimeServerPath() (string, error) {
	dir, err := runtimeCurrentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cix-server"+exeSuffix), nil
}

func runtimeCLIPath() (string, error) {
	dir, err := runtimeCurrentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cix"+exeSuffix), nil
}

func runtimeVersionDir(version string) (string, error) {
	root, err := runtimeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, version), nil
}

// readRuntimeInfo loads the manifest of the current runtime.
func readRuntimeInfo() (runtimeInfo, error) {
	dir, err := runtimeLiveDir()
	if err != nil {
		return runtimeInfo{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, runtimeManifestName))
	if err != nil {
		return runtimeInfo{}, err
	}
	var info runtimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return runtimeInfo{}, err
	}
	return info, nil
}

// runtimeAssetName is the tarball attached to server release `version`.
func runtimeAssetName(version string) string {
	return runtimeDirPrefix + version + runtimeAssetSuffix
}

// runtimeVersionFromTarball recovers the version from an asset filename —
// cix-runtime-0.12.8-darwin-arm64.tar.gz → 0.12.8. Used only for the local
// CIX_RUNTIME_TARBALL path; a downloaded release carries its version in the
// release itself.
func runtimeVersionFromTarball(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, runtimeAssetSuffix)
	if v, ok := strings.CutPrefix(name, runtimeDirPrefix); ok && v != "" {
		return v
	}
	return "local"
}

// installRuntime downloads, verifies and unpacks the runtime for a release,
// leaving it staged in its versioned directory. It does NOT touch `current` —
// activateRuntime does that, separately, so that everything fallible happens
// while the running system is still untouched.
func installRuntime(rel release.Release, progress func(string)) error {
	dest, err := runtimeVersionDir(rel.Version)
	if err != nil {
		return err
	}

	asset, ok := rel.AssetByName(runtimeAssetName(rel.Version))
	if !ok {
		// Fall back to matching by shape. A release whose tarball was named
		// from a differently-formatted version string is still installable, and
		// failing on the filename would be pedantry.
		asset, ok = rel.AssetBySuffix(runtimeAssetSuffix)
	}
	if !ok {
		return fmt.Errorf("release %s has no runtime attached", rel.TagName)
	}
	sums, ok := rel.AssetByName("checksums.txt")
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt attached", rel.TagName)
	}

	cacheDir, err := updatesCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(cacheDir)

	// GitHub reports the asset size, but a release built by hand or served by a
	// test harness may not. "(0 MB)" would be worse than saying nothing.
	if asset.Size > 0 {
		progress(fmt.Sprintf("Downloading the cix runtime (%d MB)…", (asset.Size+(1<<19))>>20))
	} else {
		progress("Downloading the cix runtime…")
	}

	tarball := filepath.Join(cacheDir, asset.Name)
	if err := download(asset.URL, tarball); err != nil {
		return fmt.Errorf("could not download the runtime: %w", err)
	}
	sumsPath := filepath.Join(cacheDir, "checksums.txt")
	if err := download(sums.URL, sumsPath); err != nil {
		return fmt.Errorf("could not download the checksums: %w", err)
	}
	if err := verifyChecksum(tarball, sumsPath, asset.Name); err != nil {
		logf("checksum verification failed for %s: %v", asset.Name, err)
		return fmt.Errorf("the downloaded runtime failed its checksum check and was discarded.\n\n%v", err)
	}

	progress("Installing the cix runtime…")
	return unpackRuntime(tarball, dest)
}

// safeJoin resolves name under dir and refuses anything that would land outside
// it — the tar equivalent of zip-slip.
//
// An absolute member name is refused rather than quietly re-rooted the way tar
// does it. filepath.Join would swallow the leading slash and produce a path
// inside dir, so this is not a containment hole; it is an archive that is not
// the one build-runtime.sh writes, and reading it as if it were is how a real
// difference gets ignored.
func safeJoin(dir, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("archive entry has an absolute path: %s", name)
	}
	clean := filepath.Clean(filepath.Join(dir, name))
	if clean != dir && !strings.HasPrefix(clean, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes the destination: %s", name)
	}
	return clean, nil
}

// pruneRuntimes deletes every installed runtime except the named ones.
//
// The previous version is deliberately among them: it is what an automatic
// rollback swaps back to, and keeping it costs ~90 MB against having to
// re-download during an incident.
func pruneRuntimes(keep ...string) {
	root, err := runtimeRoot()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	kept := map[string]bool{}
	for _, k := range keep {
		if k != "" {
			kept[k] = true
		}
	}
	for _, e := range entries {
		name := e.Name()
		if name == runtimeLinkName || kept[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			logf("could not prune runtime %s: %v", name, err)
			continue
		}
		logf("pruned runtime %s", name)
	}
}

// ensureRuntime installs the runtime when none is present.
//
// Called before anything that needs a server binary. A missing runtime is the
// normal state of a fresh install and of the first launch after upgrading from
// a version that carried its runtime inside the bundle.
func ensureRuntime(u *updater, progress func(string)) error {
	if runtimeReady() {
		return nil
	}

	// Development seam, and the only way to install a runtime for a build that
	// has no release behind it. Same shape as CIX_UPDATE_BASE_URL: unset in
	// every normal run, and nothing here needs configuring.
	if local := os.Getenv("CIX_RUNTIME_TARBALL"); local != "" {
		logf("installing runtime from CIX_RUNTIME_TARBALL=%s", local)
		// Named from the tarball rather than from the app: the two versions are
		// unrelated now, and build-runtime.sh puts the server version in the
		// filename precisely so it can be read back here.
		v := runtimeVersionFromTarball(local)
		dest, err := runtimeVersionDir(v)
		if err != nil {
			return err
		}
		if err := unpackRuntime(local, dest); err != nil {
			return err
		}
		_, err = activateRuntime(v)
		return err
	}

	// The newest published server, not one pinned to this app's version. They
	// are separate release streams on purpose: a server release should reach a
	// Mac the same day it reaches Docker Hub, without waiting for the app to be
	// re-tagged for it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rel, err := u.latestRuntime(ctx)
	if err != nil {
		return fmt.Errorf("could not find a cix server to install: %w", err)
	}
	if rel.Version == "" {
		return fmt.Errorf("no published cix server release was found to install")
	}
	if err := installRuntime(rel, progress); err != nil {
		return err
	}
	if _, err := activateRuntime(rel.Version); err != nil {
		return err
	}
	return nil
}

// runtimeSummary is the line the details submenu shows. Kept here rather than in
// status_darwin.go because it is about what is installed, not about what the
// server is currently doing — which is why it still says something useful when
// the server is stopped.
func runtimeSummary() string {
	v := currentRuntimeVersion()
	if v == "" {
		return "Server: not installed"
	}
	if info, err := readRuntimeInfo(); err == nil && info.LlamaVersion != "" {
		return fmt.Sprintf("Server %s (llama %s)", v, info.LlamaVersion)
	}
	return "Server " + v
}
