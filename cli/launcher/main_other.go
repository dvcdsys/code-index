//go:build !darwin

package main

import (
	"fmt"
	"os"
)

// cix-launcher wraps macOS-specific plumbing (menu bar, launchd, osascript,
// codesign) and has no meaning on other platforms. It still has to *compile*
// everywhere: ci-cli.yml runs `go build ./...` and `go vet ./...` on
// ubuntu-latest, and a package with no buildable files there fails with
// "build constraints exclude all Go files" — which reads like a broken repo
// rather than a deliberate platform restriction.
func main() {
	fmt.Fprintf(os.Stderr, "cix-launcher %s: macOS only — use `cix` and `cix-server` directly on this platform.\n", displayVersion())
	os.Exit(1)
}
