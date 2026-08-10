package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// bundle describes the .app this launcher is running from.
//
// The layout is fixed by mac/scripts/build-app.sh and by one hard runtime
// constraint: cix-server resolves its llama-server via
// filepath.Dir(os.Executable())/llama, so llama/ must sit next to cix-server,
// which means every executable lives in Contents/MacOS/ — Resources/ is not an
// option (codesign --verify --strict rejects executables there).
//
//	cix.app/Contents/
//	  MacOS/  cix-launcher  cix-server  cix  llama/{llama-server,*.dylib}
//	  Resources/  AppIcon.icns  menubar.png  menubar@2x.png
type bundle struct {
	Root      string // …/cix.app
	MacOS     string // …/cix.app/Contents/MacOS
	Resources string // …/cix.app/Contents/Resources
	Server    string // …/Contents/MacOS/cix-server
	CLI       string // …/Contents/MacOS/cix
	LlamaDir  string // …/Contents/MacOS/llama
}

// errNotBundled is returned when the launcher runs from a plain directory
// (`go run ./launcher`, `go build -o /tmp/x`) rather than from inside a .app.
var errNotBundled = errors.New("not running from an application bundle")

// locateBundle derives the bundle layout from the running executable.
//
// os.Executable() is resolved through symlinks deliberately: /usr/local/bin/cix
// is a symlink into the bundle (so it follows updates), and a future
// launcher symlink would otherwise resolve the layout relative to /usr/local/bin.
func locateBundle() (bundle, error) {
	exe, err := os.Executable()
	if err != nil {
		return bundle{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	macOS := filepath.Dir(exe)
	contents := filepath.Dir(macOS)
	root := filepath.Dir(contents)

	if filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(root, ".app") {
		return bundle{}, errNotBundled
	}

	return bundle{
		Root:      root,
		MacOS:     macOS,
		Resources: filepath.Join(contents, "Resources"),
		Server:    filepath.Join(macOS, "cix-server"),
		CLI:       filepath.Join(macOS, "cix"),
		LlamaDir:  filepath.Join(macOS, "llama"),
	}, nil
}

// isTranslocated reports whether macOS is running the bundle from a read-only
// randomised copy instead of where the user put it.
//
// Gatekeeper does this to any quarantined, unsigned-by-Developer-ID app opened
// from outside /Applications — typically straight out of ~/Downloads. The app
// appears to work, but the bundle path is a throwaway, so anything the launcher
// writes into it (or any launchd job pointing at it) breaks the moment the
// translocated copy is reaped. Detect it and say so, rather than half-working.
func isTranslocated(b bundle) bool {
	return strings.Contains(b.Root, "/AppTranslocation/")
}

// binaryVersion runs `<bin> <flag>` and returns its first line of output.
//
// Both bundled binaries answer a version flag and exit immediately
// (cix-server -v, cix --version), so the timeout is a guard against a wedged
// exec rather than an expected wait.
func binaryVersion(bin, flag string) string {
	if _, err := os.Stat(bin); err != nil {
		return "missing"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, flag).Output()
	if err != nil {
		return "unknown"
	}

	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" {
		return "unknown"
	}
	return line
}
