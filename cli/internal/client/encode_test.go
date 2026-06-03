package client

import (
	"crypto/sha1"
	"fmt"
	"testing"
)

// sha1Hex16 mirrors the server's projects.hashPath: first 16 hex chars of the
// SHA1 of the identity key. Kept local to the test so a drift in encodeProjectPath
// is caught against the server's documented formula, not against itself.
func sha1Hex16(s string) string {
	h := sha1.Sum([]byte(s))
	return fmt.Sprintf("%x", h)[:16]
}

// TestEncodeProjectPath_ExternalIsBare is the regression test for the prod bug
// where `cix search -n github.com/owner/repo@branch` 404'd: encodeProjectPath
// unconditionally wrapped every identity in "local:{machineID}:", but the server
// hashes EXTERNAL projects (MachineID=="") from the bare host_path. An external
// identifier is never an absolute filesystem path, so it must hash bare.
func TestEncodeProjectPath_ExternalIsBare(t *testing.T) {
	ext := "github.com/MythicalGames/pf3-backend@main"
	want := sha1Hex16(ext) // server: hashPath(host_path), no local: wrapper
	if got := encodeProjectPath(ext); got != want {
		t.Errorf("encodeProjectPath(%q) = %q, want %q (external must hash bare)", ext, got, want)
	}
}

// TestEncodeProjectPath_LocalIsNamespaced confirms the local path keeps the
// per-machine "local:{machineID}:{path}" namespacing the server applies when
// MachineID != "" (absolute filesystem paths only).
func TestEncodeProjectPath_LocalIsNamespaced(t *testing.T) {
	abs := "/Users/dev/go/src/example"
	want := sha1Hex16("local:" + machineID() + ":" + abs)
	if got := encodeProjectPath(abs); got != want {
		t.Errorf("encodeProjectPath(%q) = %q, want %q (local must be namespaced)", abs, got, want)
	}
	// And the two regimes must not collide: the same trailing string hashed
	// bare vs namespaced differs.
	if encodeProjectPath(abs) == sha1Hex16(abs) {
		t.Errorf("local path hashed bare — namespacing not applied")
	}
}
