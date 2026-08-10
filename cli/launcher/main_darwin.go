package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cix-launcher — the executable behind cix.app.
//
// Scope of this build: the .app packaging pipeline (mac/v* tag → DMG). The
// menu-bar interface, launchd control and self-updater land in later releases.
// What ships today is a correctly signed bundle carrying cix-server, the cix
// CLI and a Metal-enabled llama-server, plus enough of a front end to tell the
// user what they have and prove the bundle is intact.
func main() {
	showVersion := flag.Bool("v", false, "print version and exit")
	report := flag.Bool("report", false, "print the bundle report to stdout instead of showing a dialog")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cix-launcher %s\n", version)
		return
	}

	b, err := locateBundle()
	if err != nil {
		// Running outside a .app is a developer scenario (`go run ./launcher`),
		// not a user-facing error — there is no bundle to report on, so say so
		// on the terminal that is definitely attached and stop.
		fmt.Fprintf(os.Stderr, "cix-launcher %s: %v\n", version, err)
		os.Exit(1)
	}

	if icon := filepath.Join(b.Resources, "cix.icns"); fileExists(icon) {
		dialogIcon = icon
	}

	if isTranslocated(b) {
		msg := "macOS is running cix from a temporary read-only copy, so it cannot manage a server.\n\n" +
			"Move cix.app to your Applications folder and open it from there."
		if *report {
			fmt.Fprintln(os.Stderr, msg)
			os.Exit(1)
		}
		_ = alert("Move cix.app to Applications", msg)
		os.Exit(1)
	}

	body := bundleReport(b)

	// A .app has no terminal attached: writing to stdout on a double-click is
	// indistinguishable from crashing. -report exists so the same information
	// is scriptable for CI and for the DMG verification steps.
	if *report {
		fmt.Println(body)
		return
	}

	if err := alert("cix "+displayVersion(), body); err != nil {
		fmt.Fprintf(os.Stderr, "cix-launcher: %v\n", err)
		os.Exit(1)
	}
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
		fmt.Fprintf(&sb, "Embeddings: bundled llama-server (Metal)\n")
	} else {
		fmt.Fprintf(&sb, "Embeddings: MISSING — %s not found\n", b.LlamaDir)
	}

	sb.WriteString("\nThe menu-bar interface is not part of this release. ")
	sb.WriteString("To run the server now:\n\n")
	fmt.Fprintf(&sb, "  %s\n", b.Server)
	sb.WriteString("\nSee doc/MACOS_APP.md for the required environment variables.")

	return sb.String()
}
