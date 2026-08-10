package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/release"
)

// Self-update.
//
// The app replaces itself whole — bundle and all — rather than swapping
// individual binaries. That is not laziness: cix-server, the cix CLI and
// llama-server are versioned together and tested together, and an update that
// left one of them behind would produce a combination nobody has ever run.
// Moving the .app is also the only way "update the CLI" can mean anything,
// since the CLI on PATH is a symlink into the bundle.

//go:embed swap.sh
var swapScript []byte

// macTagPrefix selects this app's tag stream. server/v* and cli/v* live in the
// same repository and are released on their own schedules.
const macTagPrefix = "mac/v"

// updateCheckInterval throttles the automatic check. The request itself is
// nearly free — a cached ETag makes a no-change check a 304, which does not
// count against GitHub's unauthenticated hourly limit — but the limit is per
// IP and shared with everything else on the machine, so there is no reason to
// spend it more often than this.
const updateCheckInterval = 30 * time.Minute

type updater struct {
	bundle bundle
	client *release.Client

	lastCheck time.Time
	latest    release.Release
}

func newUpdater(b bundle) *updater {
	u := &updater{bundle: b, client: release.New(release.DefaultRepo, macTagPrefix)}
	u.client.ETag = loadPrefs().UpdateETag

	// Test seam. The update path replaces the running application, so the only
	// way to know it works is to run it — against a local server, with a
	// locally built image, rather than by publishing a release and hoping.
	// Unset in every normal run; there is nothing to configure here.
	if base := os.Getenv("CIX_UPDATE_BASE_URL"); base != "" {
		u.client.BaseURL = strings.TrimRight(base, "/")
		logf("update base URL overridden to %s (CIX_UPDATE_BASE_URL)", u.client.BaseURL)
	}
	return u
}

func updatesCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Caches", "cix", "updates"), nil
}

// check asks GitHub for the newest release, at most once per interval unless
// forced. Returns the release when one is newer than the running build.
func (u *updater) check(force bool) (release.Release, bool) {
	if isDevBuild() {
		// A development build is normally newer than the last release, so
		// "updating" it would be a downgrade. Nothing to offer.
		return release.Release{}, false
	}
	if !force && time.Since(u.lastCheck) < updateCheckInterval {
		return u.latest, release.IsNewer(version, u.latest.Version)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rel, err := u.client.Latest(ctx)
	switch {
	case errors.Is(err, release.ErrNotModified):
		// Nothing changed since the last check; keep what we already knew.
		u.lastCheck = time.Now()
		return u.latest, release.IsNewer(version, u.latest.Version)
	case err != nil:
		logf("update check failed: %v", err)
		return release.Release{}, false
	}

	u.lastCheck = time.Now()
	u.latest = rel
	if etag := u.client.ETag; etag != "" {
		p := loadPrefs()
		if p.UpdateETag != etag {
			p.UpdateETag = etag
			if err := savePrefs(p); err != nil {
				logf("could not persist the update ETag: %v", err)
			}
		}
	}
	return rel, release.IsNewer(version, rel.Version)
}

// install downloads, verifies and stages the release, then hands over to the
// swap script and quits.
//
// Every step before the swap is reversible: a failure leaves the installed app
// exactly as it was, which is why the staging directory is built and validated
// in full before anything live is touched.
func (u *updater) install(rel release.Release, serverWasRunning bool) error {
	dmgAsset, ok := rel.AssetBySuffix(".dmg")
	if !ok {
		return fmt.Errorf("release %s has no disk image attached", rel.TagName)
	}
	sumsAsset, ok := rel.AssetByName("checksums.txt")
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt attached", rel.TagName)
	}

	// Preflight before spending a download. Writing into the bundle's parent is
	// what the swap ultimately needs; find out now, while the app is untouched
	// and the message can still be "reinstall from the DMG".
	parent := filepath.Dir(u.bundle.Root)
	if err := checkWritable(parent); err != nil {
		return fmt.Errorf("cix cannot update itself because %s is not writable by you.\n\n"+
			"Download the new version and drag it over the old one instead.", parent)
	}

	cacheDir, err := updatesCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(cacheDir)

	dmgPath := filepath.Join(cacheDir, dmgAsset.Name)
	if err := download(dmgAsset.URL, dmgPath); err != nil {
		return fmt.Errorf("could not download the update: %w", err)
	}
	sumsPath := filepath.Join(cacheDir, "checksums.txt")
	if err := download(sumsAsset.URL, sumsPath); err != nil {
		return fmt.Errorf("could not download the checksums: %w", err)
	}

	if err := verifyChecksum(dmgPath, sumsPath, dmgAsset.Name); err != nil {
		// With no Developer ID signature this checksum is the entire integrity
		// story, so a mismatch is fatal and loud rather than a warning.
		logf("checksum verification failed for %s: %v", dmgAsset.Name, err)
		return fmt.Errorf("the downloaded update failed its checksum check and was discarded.\n\n%v", err)
	}

	staged := filepath.Join(parent, ".cix.app.new")
	os.RemoveAll(staged)
	if err := stageFromDMG(dmgPath, staged); err != nil {
		os.RemoveAll(staged)
		return err
	}

	// Record the intent before quitting: the process that acts on it is the one
	// that starts after the swap, and it has no other way to know the server
	// was up.
	p := loadPrefs()
	p.RestartServerAfterUpdate = serverWasRunning
	if err := savePrefs(p); err != nil {
		logf("could not record the post-update restart flag: %v", err)
	}

	// The old cix-server's executable lives inside the bundle about to be
	// moved. macOS kills a process whose signed binary is replaced underneath
	// it, so this is a controlled stop rather than a surprise SIGKILL.
	if serverWasRunning {
		if err := stopServer(); err != nil {
			logf("could not stop the server before the swap: %v", err)
		}
		waitForServerStop(20 * time.Second)
	}

	return u.launchSwap(staged)
}

// launchSwap writes the swap script to a temp file and starts it detached.
func (u *updater) launchSwap(staged string) error {
	dir, err := os.MkdirTemp("", "cix-update-")
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "swap.sh")
	if err := os.WriteFile(script, swapScript, 0o755); err != nil {
		return err
	}

	logPath, err := launcherLogPath()
	if err != nil {
		logPath = os.DevNull
	}

	cmd := exec.Command("/bin/bash", script,
		fmt.Sprint(os.Getpid()), u.bundle.Root, staged, logPath)
	// Its own session, so it is not taken down with the launcher it is waiting
	// for — which is the entire job.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the update helper: %w", err)
	}
	// Release the child rather than waiting: this process is about to exit and
	// the helper must outlive it.
	_ = cmd.Process.Release()
	logf("update staged at %s; swap helper started, quitting", staged)
	return nil
}

// stageFromDMG mounts the image, copies the app out, and validates it.
func stageFromDMG(dmgPath, staged string) error {
	out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", dmgPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not open the downloaded disk image: %s", strings.TrimSpace(string(out)))
	}
	mount := parseMountPoint(string(out))
	if mount == "" {
		return fmt.Errorf("could not determine where the disk image was mounted")
	}
	defer exec.Command("hdiutil", "detach", mount, "-force", "-quiet").Run()

	src := filepath.Join(mount, "cix.app")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("the disk image does not contain cix.app")
	}

	// ditto, not cp -R: it preserves the extended attributes and metadata a
	// code signature is sealed over. cp -R drops some of them, and the bundle
	// then fails the verification two lines below.
	if out, err := exec.Command("ditto", src, staged).CombinedOutput(); err != nil {
		return fmt.Errorf("could not copy the new version: %s", strings.TrimSpace(string(out)))
	}

	// Quarantine came from the download; left in place, the nested llama-server
	// is SIGKILLed on exec with empty stderr.
	_ = exec.Command("xattr", "-cr", staged).Run()

	if out, err := exec.Command("codesign", "--verify", "--strict", staged).CombinedOutput(); err != nil {
		return fmt.Errorf("the downloaded application failed signature verification and was discarded: %s",
			strings.TrimSpace(string(out)))
	}
	return nil
}

// parseMountPoint pulls /Volumes/... out of hdiutil attach's output.
func parseMountPoint(out string) string {
	var mount string
	for line := range strings.SplitSeq(out, "\n") {
		if i := strings.Index(line, "/Volumes/"); i >= 0 {
			mount = strings.TrimSpace(line[i:])
		}
	}
	return mount
}

func download(url, dest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// verifyChecksum compares the file against its line in a shasum-format list.
//
// Worth being explicit about what this does and does not prove: it establishes
// that the bytes on disk are the bytes the release lists, which catches a
// truncated or corrupted download. It is not a trust anchor — checksums.txt
// arrives over the same channel as the image, so anyone able to replace one can
// replace the other. Without a Developer ID signature there is nothing better
// available here; a detached signature over the checksums would be.
func verifyChecksum(path, sumsPath, name string) error {
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	want, err := checksumFor(string(sums), name)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("expected %s, got %s", want, got)
	}
	return nil
}

// checksumFor finds a file's hash in shasum output.
//
// The name is matched against the last path component because the release
// workflow generates the file with `shasum -a 256 ./*.dmg`, which records
// "./cix-0.3.1-arm64.dmg" — a leading ./ that a plain equality check would miss.
func checksumFor(sums, name string) (string, error) {
	for line := range strings.SplitSeq(sums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[len(fields)-1]) == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// checkWritable reports whether the current user can create files in dir.
//
// Deliberately not answered by escalating: an unsigned application asking for
// an administrator password in order to overwrite itself is indistinguishable
// from malware, and teaching users to approve that is worse than an update that
// asks them to drag an icon.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".cix-write-test-")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func waitForServerStop(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if launchdPID() == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}
