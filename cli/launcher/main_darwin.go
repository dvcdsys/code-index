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
		handleForeignAgent(b)

	case needsFirstRun():
		if err := runFirstRun(b); err != nil {
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
		// Re-point the launchd agent at this bundle, preserving the user's
		// autostart choice. An app that was moved, or replaced by an update,
		// would otherwise keep a job aimed at a path that no longer holds the
		// binary.
		if err := writeLaunchdFiles(b, autostartEnabled()); err != nil {
			logf("could not refresh launchd files: %v", err)
		}
	}

	runMenu(b)
}

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

// bundleReport asks each bundled binary for its own version rather than
// printing what the build script believed it packaged. That difference is the
// point: it catches a bundle assembled from stale dist/ artefacts, which is the
// failure this pipeline is most likely to produce.
func bundleReport(b bundle) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Launcher:   cix-launcher %s\n", version)
	fmt.Fprintf(&sb, "Server:     %s\n", binaryVersion(b.Server, "-v"))
	fmt.Fprintf(&sb, "CLI:        %s\n", binaryVersion(b.CLI, "--version"))

	if _, err := os.Stat(b.LlamaDir); err == nil {
		sb.WriteString("Embeddings: bundled llama-server (Metal)\n")
	} else {
		fmt.Fprintf(&sb, "Embeddings: MISSING — %s not found\n", b.LlamaDir)
	}

	return sb.String()
}
