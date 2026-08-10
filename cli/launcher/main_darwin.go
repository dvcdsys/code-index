package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cix-launcher — the executable behind cix.app.
//
// A menu bar item showing whether the local cix server is running and what its
// embedding provider is doing, with start/stop and a dashboard link. It is an
// LSUIElement app: no Dock icon, no windows, no application menu.
func main() {
	showVersion := flag.Bool("v", false, "print version and exit")
	report := flag.Bool("report", false, "print a bundle report to stdout and exit, instead of showing the menu")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cix-launcher %s\n", version)
		return
	}

	b, err := locateBundle()
	if err != nil {
		// Running outside a .app is a developer scenario (`go run ./launcher`),
		// not a user-facing error — there is no bundle to manage, so say so on
		// the terminal that is definitely attached and stop.
		fmt.Fprintf(os.Stderr, "cix-launcher %s: %v\n", version, err)
		os.Exit(1)
	}

	if icon := filepath.Join(b.Resources, "cix.icns"); fileExists(icon) {
		dialogIcon = icon
	}

	// -report is the scriptable path: CI verifies a freshly built bundle with
	// it, and it never opens a window.
	if *report {
		fmt.Println(bundleReport(b))
		return
	}

	// One updater for the process: it owns the cached ETag and the release
	// client, and both the runtime installer and the menu's update check need
	// them. Two of these would mean two ETags racing over one preferences file.
	u := newUpdater(b)

	if isTranslocated(b) {
		// Gatekeeper is running the app from a randomised read-only copy, which
		// it does to any quarantined app opened from outside /Applications.
		// Everything appears to work until the copy is reaped, taking the
		// launchd job's target with it — so refuse rather than half-work.
		_ = alert("Move cix to Applications",
			"macOS is running cix from a temporary copy, so it cannot manage a server.\n\n"+
				"Move cix.app to your Applications folder and open it from there.")
		os.Exit(1)
	}

	stripQuarantine(b)

	// Order matters here. A machine that already runs cix from a checkout has a
	// launchd agent under our label and a server holding our port, so the
	// first-run wizard must never get a look at it — it would set up a second
	// server that cannot bind, against a second, empty database.
	switch {
	case foreignAgent():
		// Asks once, remembers the answer, and defaults to leaving it alone.
		// When the user declines, the app stays in observe-only mode: status
		// and the dashboard work, Start/Stop do not.
		//
		// No runtime is installed on this path. Observing somebody else's server
		// needs no binaries of our own, and downloading 90 MB to watch an
		// install the user asked us not to touch would be presumptuous. The one
		// feature that does need it — password reset — offers the download when
		// it is used.
		handleForeignAgent(u)

	case needsFirstRun():
		if err := runFirstRun(u); err != nil {
			if errors.Is(err, errCancelled) {
				// Setup is resumable: the app stays in the menu bar with Start
				// disabled, and the next launch offers the wizard again.
				_ = alert("Setup cancelled",
					"cix has not been set up yet, so the server cannot start.\n\n"+
						"Quit and reopen cix when you want to finish setting it up.")
			} else {
				logf("first-run setup failed: %v", err)
				_ = alert("Setup failed", fmt.Sprintf("cix could not complete first-time setup.\n\n%v", err))
			}
		}

	default:
		// A configured install with no runtime is the first launch after
		// upgrading from a version that carried its server inside the bundle:
		// the old app is gone, and with it the binary the launchd wrapper was
		// pointing at. Say so before spending a minute on a download, because
		// otherwise this is a menu bar app that appears to do nothing at all.
		//
		// Not said when a local tarball is supplied: there is no download to
		// warn about, and this is the path a developer takes on every build.
		if !runtimeReady() && os.Getenv("CIX_RUNTIME_TARBALL") == "" {
			_ = alert("cix needs to finish updating",
				"cix now keeps its server outside the application, so it updates without restarting.\n\n"+
					"It will download that part now — around 40 MB, once. The menu bar icon appears when it is done.")
		}
		if err := ensureRuntime(u, logProgress); err != nil {
			logf("could not install the runtime: %v", err)
			_ = alert("cix could not install its server",
				fmt.Sprintf("%v\n\nThe menu bar app still works, but the server cannot start until this succeeds.", err))
			break
		}
		// Only after the runtime exists: pointing the launchd wrapper at a
		// binary that is not there would break a working install rather than
		// leave it alone.
		//
		// Nothing is started here. An app update no longer stops the server —
		// the bundle holds none of what it runs — so there is no interrupted
		// state to resume, and starting a server the user had deliberately
		// stopped would be the app overriding them.
		if err := writeLaunchdFiles(autostartEnabled()); err != nil {
			logf("could not refresh launchd files: %v", err)
		}
	}

	runMenu(b, u)
}

// logProgress is the progress sink for work that happens before the menu bar
// item exists. There is nowhere to show it, so it goes in the log — which is
// where anyone investigating a slow first launch will look.
func logProgress(msg string) { logf("%s", msg) }

// stripQuarantine clears com.apple.quarantine from the whole bundle, once.
//
// Not cosmetic: the nested llama-server inherits the quarantine flag from the
// disk image, and macOS then SIGKILLs it on exec with EMPTY STDERR. From the
// supervisor's side that is indistinguishable from a crash, which is precisely
// the failure server/Makefile documents for stale signatures. Best-effort — on
// a read-only volume there is nothing to do and nothing to report.
func stripQuarantine(b bundle) {
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", b.Root).Run()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// bundleReport prints what this installation actually consists of: the app, and
// whichever runtime it is pointing at.
//
// The runtime half asks the binaries for their own versions rather than
// trusting runtime.json, because the difference is the point — it catches a
// runtime assembled from stale dist/ artefacts, which is the failure this
// pipeline is most likely to produce. CI runs this against a freshly built app,
// where the answer is "no runtime", and that is correct: the two halves of a
// release are built separately and only meet on a user's machine.
func bundleReport(b bundle) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Launcher:   cix-launcher %s\n", version)
	fmt.Fprintf(&sb, "Bundle:     %s\n", b.Root)

	if !runtimeReady() {
		sb.WriteString("Runtime:    not installed\n")
		return sb.String()
	}

	dir, _ := runtimeCurrentDir()
	fmt.Fprintf(&sb, "Runtime:    %s (%s)\n", currentRuntimeVersion(), dir)
	fmt.Fprintf(&sb, "Server:     %s\n", binaryVersion(filepath.Join(dir, "cix-server"), "-v"))
	fmt.Fprintf(&sb, "CLI:        %s\n", binaryVersion(filepath.Join(dir, "cix"), "--version"))

	llama := filepath.Join(dir, "llama")
	if _, err := os.Stat(llama); err == nil {
		info, _ := readRuntimeInfo()
		fmt.Fprintf(&sb, "Embeddings: llama-server %s (Metal)\n", info.LlamaVersion)
	} else {
		fmt.Fprintf(&sb, "Embeddings: MISSING — %s not found\n", llama)
	}

	return sb.String()
}
