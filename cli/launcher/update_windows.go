package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/dvcdsys/code-index/cli/internal/release"
)

// Windows half of self-update: the launcher is replaced by running the new
// release's installer silently once this process has exited. The shared half
// (the two release streams, the runtime swap and rollback, download and
// checksum) is in update.go.
//
// Why the installer rather than a swap script like macOS. The installer is the
// one thing that already knows how to put every file in place, update the
// shortcuts and uninstall entry, and replace an executable that Windows would
// otherwise keep locked. It runs per-user, so — as on macOS — nothing here ever
// asks for an administrator password.

// appTagPrefix is the launcher's release stream: the installer ships on win/v*.
const appTagPrefix = "win/v"

// installerSuffix matches the setup asset, cix-<version>-x64-setup.exe.
const installerSuffix = "-x64-setup.exe"

func updatesCacheDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "cix", "updates"), nil
}

// updateLauncher downloads and verifies the new installer, then starts it
// detached. The caller quits immediately afterwards; the installer waits for
// this process to go (it declares our singleton mutex as AppMutex) and then
// relaunches the app.
//
// Every step before the launch is reversible: a failure leaves the installed
// app exactly as it was.
func (u *updater) updateLauncher(rel release.Release, progress func(string)) error {
	setup, err := u.stageUpdate(rel, progress)
	if err != nil {
		return err
	}
	return launchInstaller(setup)
}

// stageUpdate downloads the installer and checks it against checksums.txt.
// Split from updateLauncher so everything that can fail is reachable from a test:
// the launch ends the process, and a test cannot wait for its own exit.
func (u *updater) stageUpdate(rel release.Release, progress func(string)) (string, error) {
	defer progress("")
	progress("Downloading the cix update…")

	asset, ok := rel.AssetBySuffix(installerSuffix)
	if !ok {
		return "", fmt.Errorf("release %s has no installer attached", rel.TagName)
	}
	sums, ok := rel.AssetByName("checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s has no checksums.txt attached", rel.TagName)
	}

	// Not deleted afterwards: the installer is still running from here when this
	// process exits. It sits in the updates cache and is replaced next time.
	cacheDir, err := updatesCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}

	setupPath := filepath.Join(cacheDir, asset.Name)
	if err := download(asset.URL, setupPath); err != nil {
		return "", fmt.Errorf("could not download the update: %w", err)
	}
	sumsPath := filepath.Join(cacheDir, "checksums.txt")
	if err := download(sums.URL, sumsPath); err != nil {
		return "", fmt.Errorf("could not download the checksums: %w", err)
	}
	if err := verifyChecksum(setupPath, sumsPath, asset.Name); err != nil {
		logf("checksum verification failed for %s: %v", asset.Name, err)
		os.Remove(setupPath)
		return "", fmt.Errorf("the downloaded update failed its checksum check and was discarded.\n\n%v", err)
	}
	return setupPath, nil
}

// launchInstaller starts the setup program silently and detached.
func launchInstaller(setup string) error {
	logPath, err := launcherLogPath()
	if err != nil {
		logPath = filepath.Join(os.TempDir(), "cix-launcher.log")
	}
	installLog := strings.TrimSuffix(logPath, ".log") + "-setup.log"

	// /RELAUNCH=1 is read by the installer's script: a silent install only
	// reopens the app when asked, so an unattended install does not pop it up.
	cmd := exec.Command(setup, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/RELAUNCH=1", "/LOG="+installLog)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Its own process group and no console, so it is not taken down with the
		// launcher it is waiting for — which is the entire job.
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the installer: %w", err)
	}
	_ = cmd.Process.Release()
	logf("update installer started (%s); quitting", setup)
	return nil
}
