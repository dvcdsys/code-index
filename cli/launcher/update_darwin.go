package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dvcdsys/code-index/cli/internal/release"
)

// macOS half of self-update: the .app is replaced by a detached bash helper
// once this process has exited. The shared half (the two release streams, the
// runtime swap and rollback, download and checksum) is in update.go.

//go:embed swap.sh
var swapScript []byte

// appTagPrefix is the launcher's release stream: cix.app ships on mac/v*.
const appTagPrefix = "mac/v"

func updatesCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Caches", "cix", "updates"), nil
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
	staged, err := u.stageUpdate(rel, progress)
	if err != nil {
		return err
	}
	return u.launchSwap(staged)
}

// stageUpdate downloads and validates the new .app, leaving it beside the
// installed one ready to be moved into place. Split from updateLauncher so
// everything that can fail is reachable from a test: the swap itself ends the
// process, and a test cannot wait for its own exit.
func (u *updater) stageUpdate(rel release.Release, progress func(string)) (string, error) {
	defer progress("")
	progress("Downloading the cix update…")

	dmgAsset, ok := rel.AssetBySuffix(".dmg")
	if !ok {
		return "", fmt.Errorf("release %s has no disk image attached", rel.TagName)
	}
	sumsAsset, ok := rel.AssetByName("checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s has no checksums.txt attached", rel.TagName)
	}

	// Preflight before spending a download. Writing into the bundle's parent is
	// what the swap ultimately needs; find out now, while the app is untouched
	// and the message can still be "reinstall from the DMG".
	parent := filepath.Dir(u.bundle.Root)
	if err := checkWritable(parent); err != nil {
		return "", fmt.Errorf("cix cannot update itself because %s is not writable by you.\n\n"+
			"Download the new version and drag it over the old one instead.", parent)
	}

	cacheDir, err := updatesCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(cacheDir)

	dmgPath := filepath.Join(cacheDir, dmgAsset.Name)
	if err := download(dmgAsset.URL, dmgPath); err != nil {
		return "", fmt.Errorf("could not download the update: %w", err)
	}
	sumsPath := filepath.Join(cacheDir, "checksums.txt")
	if err := download(sumsAsset.URL, sumsPath); err != nil {
		return "", fmt.Errorf("could not download the checksums: %w", err)
	}

	if err := verifyChecksum(dmgPath, sumsPath, dmgAsset.Name); err != nil {
		// With no Developer ID signature this checksum is the entire integrity
		// story, so a mismatch is fatal and loud rather than a warning.
		logf("checksum verification failed for %s: %v", dmgAsset.Name, err)
		return "", fmt.Errorf("the downloaded update failed its checksum check and was discarded.\n\n%v", err)
	}

	staged := filepath.Join(parent, ".cix.app.new")
	os.RemoveAll(staged)
	if err := stageFromDMG(dmgPath, staged); err != nil {
		os.RemoveAll(staged)
		return "", err
	}
	return staged, nil
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
