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

	"github.com/dvcdsys/code-index/cli/internal/client"
	"github.com/dvcdsys/code-index/cli/internal/release"
)

// Self-update.
//
// A release has two halves, published together under one mac/vX.Y.Z tag and
// updated independently:
//
//   - The runtime — cix-server, the cix CLI, llama-server — installed into
//     ~/.cix/runtime/<version>/ and switched on by moving a symlink. The app
//     keeps running throughout, and a runtime that does not come back up is
//     rolled back automatically.
//   - The launcher — the .app itself — which can only be replaced by a detached
//     helper that waits for this process to exit, because a process cannot
//     overwrite its own signed executable and survive.
//
// In the normal case both change at once and both are done, runtime first: it
// is the reversible half, so a failure there costs nothing, whereas the launcher
// swap ends this process.
//
// The runtime carries the CLI rather than the app, so `cix` on PATH is a symlink
// into ~/.cix/runtime/current and follows updates without /usr/local being
// touched.

//go:embed swap.sh
var swapScript []byte

// The two streams this app watches. They are separate releases of separate
// things and neither waits for the other: a server release reaches a Mac the
// same day it reaches Docker Hub, and the app ships when the app changes.
const (
	macTagPrefix    = "mac/v"    // cix.app — the launcher
	serverTagPrefix = "server/v" // the runtime, same tag as the Docker images
)

// updateCheckInterval throttles the automatic check. The requests are nearly
// free — a cached ETag makes a no-change check a 304, which does not count
// against GitHub's unauthenticated hourly limit — but the limit is per IP and
// shared with everything else on the machine, and this is now two requests
// rather than one, so there is no reason to spend them more often than this.
const updateCheckInterval = 30 * time.Minute

type updater struct {
	bundle bundle

	app     *release.Client
	runtime *release.Client

	lastCheck time.Time

	// The newest release seen on each stream, whether or not it is newer than
	// what is installed. Kept so a 304 — which carries no body — leaves the
	// previous answer standing instead of reading as "nothing published".
	seenApp     release.Release
	seenRuntime release.Release
}

// available is what a check turned up: the halves that are behind, if any. A
// zero Release means that half is current.
type available struct {
	App     release.Release
	Runtime release.Release
}

func (a available) any() bool { return a.App.Version != "" || a.Runtime.Version != "" }

func newUpdater(b bundle) *updater {
	p := loadPrefs()
	u := &updater{
		bundle:  b,
		app:     release.New(release.DefaultRepo, macTagPrefix),
		runtime: release.New(release.DefaultRepo, serverTagPrefix),
	}
	u.app.ETag = p.UpdateETag
	u.runtime.ETag = p.RuntimeETag

	// Test seam. The update path replaces the running application and restarts
	// the server, so the only way to know it works is to run it — against a
	// local server, with locally built artefacts, rather than by publishing a
	// release and hoping. Unset in every normal run; nothing here needs
	// configuring.
	if base := os.Getenv("CIX_UPDATE_BASE_URL"); base != "" {
		trimmed := strings.TrimRight(base, "/")
		u.app.BaseURL = trimmed
		u.runtime.BaseURL = trimmed
		logf("update base URL overridden to %s (CIX_UPDATE_BASE_URL)", trimmed)
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

// check asks GitHub about both streams, at most once per interval unless forced.
func (u *updater) check(force bool) available {
	if force || time.Since(u.lastCheck) >= updateCheckInterval {
		u.lastCheck = time.Now()
		u.seenApp = u.refresh("app", u.app, u.seenApp, func(p *prefs, etag string) { p.UpdateETag = etag })
		u.seenRuntime = u.refresh("runtime", u.runtime, u.seenRuntime, func(p *prefs, etag string) { p.RuntimeETag = etag })
	}

	var av available
	// A development build reports version "dev", which IsNewer refuses to
	// compare — so an unstamped launcher never offers to "update" itself to a
	// release that is probably older than the tree it was built from. Its
	// runtime is a separate question and is still offered.
	if release.IsNewer(version, u.seenApp.Version) {
		av.App = u.seenApp
	}
	if release.IsNewer(currentRuntimeVersion(), u.seenRuntime.Version) {
		av.Runtime = u.seenRuntime
	}
	return av
}

// refresh polls one stream, keeping the previous answer when nothing came back.
func (u *updater) refresh(what string, c *release.Client, previous release.Release, setETag func(*prefs, string)) release.Release {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rel, err := c.Latest(ctx)
	switch {
	case errors.Is(err, release.ErrNotModified):
		return previous
	case err != nil:
		logf("%s update check failed: %v", what, err)
		return previous
	}

	if etag := c.ETag; etag != "" {
		p := loadPrefs()
		before := p
		setETag(&p, etag)
		if p != before {
			if err := savePrefs(p); err != nil {
				logf("could not persist the %s update ETag: %v", what, err)
			}
		}
	}
	return rel
}

// install applies whichever halves are behind, runtime first.
//
// Runtime first because it is the reversible one: a failure there leaves the
// app untouched and the old server running, whereas the launcher swap ends this
// process. Returns quit=true when the launcher was staged — from that point the
// detached helper owns the outcome and waits for this process to exit.
func (u *updater) install(av available, serverWasRunning bool, progress func(string)) (quit bool, err error) {
	if av.Runtime.Version != "" {
		if err := u.updateRuntime(av.Runtime, serverWasRunning, progress); err != nil {
			return false, err
		}
	}
	if av.App.Version == "" {
		return false, nil
	}
	if err := u.updateLauncher(av.App, progress); err != nil {
		return false, err
	}
	return true, nil
}

// updateRuntime installs the new runtime and switches to it, putting the old one
// back if the server does not survive.
//
// Ordering is the whole design: everything that can fail — download, checksum,
// unpack, signature check, a test exec — happens before the running server is
// touched. Only then is the symlink moved, and moving it back is a rename.
func (u *updater) updateRuntime(rel release.Release, serverWasRunning bool, progress func(string)) error {
	defer progress("")

	if err := installRuntime(rel, progress); err != nil {
		return err
	}

	// Stopped first, deliberately. The running server's llama-server sidecar is
	// resolved relative to its own executable, so a swap underneath a live
	// process would leave the two halves of one runtime disagreeing about which
	// version they are.
	if serverWasRunning {
		progress("Restarting the cix server…")
		if err := stopServer(); err != nil {
			logf("could not stop the server before the runtime swap: %v", err)
		}
		waitForServerStop(20 * time.Second)
	}

	previous, err := activateRuntime(rel.Version)
	if err != nil {
		return fmt.Errorf("could not switch to the new runtime: %w", err)
	}

	if serverWasRunning {
		vars, _ := readServerEnv()
		if err := startServer(); err != nil || !serverSurvivedRestart(localBaseURL(vars), runtimeHealthWindow) {
			logf("runtime %s did not come back up (start error: %v); rolling back to %q", rel.Version, err, previous)
			return u.rollbackRuntime(previous, rel.Version)
		}
	}

	// Keep the version just replaced. It is what a rollback needs, and ~90 MB is
	// a cheap insurance premium against having to download during an incident.
	pruneRuntimes(rel.Version, previous)
	logf("runtime updated to %s", rel.Version)
	return nil
}

// runtimeHealthWindow is how long a freshly swapped server gets to answer.
//
// Not a deadline for "is it working" — a cold start loads an embedding model and
// can legitimately take minutes. It is a deadline for deciding, and the decision
// falls back to whether the process is still alive. See serverSurvivedRestart.
const runtimeHealthWindow = 45 * time.Second

// rollbackRuntime returns to the previous runtime after a failed update.
func (u *updater) rollbackRuntime(previous, failed string) error {
	if previous == "" || previous == failed {
		// Nothing to go back to — a first install that will not run. Leave the
		// directory in place: it is the only copy, and re-downloading it to
		// investigate would be worse than 90 MB.
		return fmt.Errorf("the new server (%s) did not start, and there is no previous version to fall back to.\n\n"+
			"See ~/.cix/logs/cix-server.err.", failed)
	}

	_ = stopServer()
	waitForServerStop(20 * time.Second)

	if _, err := activateRuntime(previous); err != nil {
		return fmt.Errorf("the new server (%s) did not start, and cix could not switch back to %s: %v",
			failed, previous, err)
	}
	if err := startServer(); err != nil {
		return fmt.Errorf("the new server (%s) did not start. cix switched back to %s, "+
			"but could not restart it: %v", failed, previous, err)
	}

	// Drop the version that just failed, and with it whatever else was lying
	// around: the machine is back on a version known to work, and there is
	// nothing to fall back FROM any more. Keeping a runtime that has been
	// demonstrated not to start is 90 MB and an entry in ~/.cix/runtime that
	// invites someone to point `current` at it by hand.
	pruneRuntimes(previous)

	return fmt.Errorf("the new server (%s) did not start, so cix went back to %s.\n\n"+
		"The update was not applied. See ~/.cix/logs/cix-server.err.", failed, previous)
}

// serverSurvivedRestart decides whether a just-started server is alive.
//
// Answering "healthy within the window" alone would be wrong: a cold start loads
// an embedding model and stays silent for minutes, and rolling a good runtime
// back for that would be worse than the bug it guards against. A process that
// has exited, on the other hand, is unambiguous — KeepAlive is false, so nothing
// restarts it and a crash leaves no pid behind.
func serverSurvivedRestart(baseURL string, timeout time.Duration) bool {
	c := client.New(baseURL, "")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Health() == nil {
			return true
		}
		time.Sleep(time.Second)
	}
	return launchdPID() != 0
}

// updateLauncher downloads, verifies and stages the new .app, then hands over to
// the swap script. The caller quits immediately afterwards.
//
// Every step before the swap is reversible: a failure leaves the installed app
// exactly as it was, which is why the staging directory is built and validated
// in full before anything live is touched.
//
// The server is not stopped for this, and used to be. Nothing a running server
// touches is inside the bundle any more — the binary, its llama sidecar and the
// launchd wrapper all live under ~/.cix — so replacing the .app is invisible to
// it. Updating the app no longer interrupts indexing.
func (u *updater) updateLauncher(rel release.Release, progress func(string)) error {
	defer progress("")
	progress("Downloading the cix update…")

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
