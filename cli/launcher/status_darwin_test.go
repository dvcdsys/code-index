package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/cli/internal/client"
)

func TestProviderLabel(t *testing.T) {
	tests := map[string]string{
		// The bundled llama-server supervisor. Showing the raw kind here would
		// tell the user they are running Ollama, which is not installed and
		// never was — see the comment on providerLabel.
		"ollama": "llama.cpp (bundled)",
		"openai": "openai",
		"voyage": "voyage",
		"":       "unknown",
	}
	for kind, want := range tests {
		if got := providerLabel(kind); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

// buildPanelState is the whole contract between the poller and panel.html —
// every field the JavaScript renders comes through it.
func TestBuildPanelState(t *testing.T) {
	running := snapshot{
		State: stateRunning, PID: 4242, Port: 21847,
		Managed: true, LocalOnly: true, Autostart: true,
		Status: &client.StatusResponse{
			ServerVersion:      "0.12.9",
			EmbeddingProvider:  "ollama",
			EmbeddingModel:     "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF",
			ActiveIndexingJobs: 2,
			Projects:           14,
		},
		EmbeddingsOK: true,
	}
	ps := buildPanelState(running, false, "")
	if ps.State != "running" || ps.PID != 4242 || ps.Port != 21847 {
		t.Errorf("state/pid/port = %q/%d/%d, want running/4242/21847", ps.State, ps.PID, ps.Port)
	}
	// The provider kind is translated, and the model loses the misleading
	// provider prefix — same reasoning as providerLabel.
	if ps.Engine != "llama.cpp (bundled)" || !ps.EngineReady {
		t.Errorf("engine = %q ready=%v, want the bundled label and ready", ps.Engine, ps.EngineReady)
	}
	if ps.Model != "awhiteside/CodeRankEmbed-Q8_0-GGUF" {
		t.Errorf("model = %q, want the prefix stripped", ps.Model)
	}
	if ps.ServerVersion != "0.12.9" || ps.IndexingJobs != 2 || ps.Projects != 14 {
		t.Errorf("version/jobs/projects = %q/%d/%d", ps.ServerVersion, ps.IndexingJobs, ps.Projects)
	}
	if !ps.LocalOnly || ps.LanAddr != "" {
		t.Errorf("a loopback-bound server must not advertise a LAN address, got %q", ps.LanAddr)
	}
	if !ps.Autostart {
		t.Error("autostart lost on the way through")
	}

	// Provider details from a server that is not answering are stale by
	// definition, so none are claimed.
	starting := buildPanelState(snapshot{State: stateStarting, Managed: true}, false, "")
	if starting.State != "starting" || starting.Engine != "" || starting.Model != "" {
		t.Errorf("starting = %+v, want no provider details", starting)
	}

	// The busy flag is the menu's, not the poller's; it must pass through with
	// its label — the flag is what the panel disables its controls on, and the
	// label is the only thing telling the user which slow operation is running.
	busy := buildPanelState(running, true, "Stopping the server…")
	if !busy.Busy || busy.BusyLabel != "Stopping the server…" {
		t.Errorf("busy/label = %v/%q, want them passed through", busy.Busy, busy.BusyLabel)
	}

	stopped := buildPanelState(snapshot{State: stateStopped, Managed: false}, false, "")
	if stopped.State != "stopped" || stopped.Managed {
		t.Errorf("stopped external = %+v", stopped)
	}
}

// formatEtime parses everything ps -o etime is documented to print.
func TestFormatEtime(t *testing.T) {
	tests := map[string]string{
		"04:12":       "4m",
		"00:42":       "0m",
		"4:12:33":     "4h 12m",
		"04:12:33":    "4h 12m",
		"2-03:04:05":  "2d 3h",
		"12-00:00:01": "12d 0h",
		"":            "",
		"garbage":     "",
	}
	for in, want := range tests {
		if got := formatEtime(in); got != want {
			t.Errorf("formatEtime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPollerDebouncesNotReady(t *testing.T) {
	// model_loaded is computed server-side under a 500 ms deadline it can
	// legitimately lose under load. One false is not evidence; flipping the
	// menu row on it produces a flicker while nothing is wrong.
	p := newPoller()
	p.snap.EmbeddingsOK = true

	notReady := &client.StatusResponse{
		EmbeddingProvider:               "ollama",
		EmbeddingProviderManagesProcess: true,
		ModelLoaded:                     false,
	}

	p.applyStatus(notReady)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK flipped after a single not-ready poll; want it held until the second")
	}

	p.applyStatus(notReady)
	if p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK still true after two consecutive not-ready polls")
	}

	// One good poll clears the count, so a later single miss cannot flip it.
	p.applyStatus(&client.StatusResponse{
		EmbeddingProvider:               "ollama",
		EmbeddingProviderManagesProcess: true,
		ModelLoaded:                     true,
	})
	if !p.snapshotNow().EmbeddingsOK {
		t.Fatal("EmbeddingsOK false after a ready poll")
	}
	p.applyStatus(notReady)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("a single not-ready poll after a ready one flipped the row")
	}
}

func TestPollerTrustsHTTPProviders(t *testing.T) {
	// openai / voyage have no local process to be down, and the server skips
	// the liveness probe for them. Debouncing to "not ready" there would show a
	// permanently red row for a provider that works.
	p := newPoller()
	http := &client.StatusResponse{
		EmbeddingProvider:               "voyage",
		EmbeddingProviderManagesProcess: false,
		ModelLoaded:                     false,
	}
	p.applyStatus(http)
	p.applyStatus(http)
	p.applyStatus(http)
	if !p.snapshotNow().EmbeddingsOK {
		t.Error("EmbeddingsOK = false for an HTTP provider; want true regardless of model_loaded")
	}
}

// ModelName keeps the untruncated id — panel.html shows it whole and relies on
// CSS to fit it, so the only transformation allowed here is the prefix strip.
func TestModelName(t *testing.T) {
	s := snapshot{Status: &client.StatusResponse{
		EmbeddingModel: "ollama:some-extremely-long-organisation/an-even-longer-model-name-v2",
	}}
	if got, want := s.ModelName(), "some-extremely-long-organisation/an-even-longer-model-name-v2"; got != want {
		t.Errorf("ModelName() = %q, want %q", got, want)
	}
	if got := (snapshot{}).ModelName(); got != "" {
		t.Errorf("ModelName() with no status = %q, want empty", got)
	}
}

func TestIsLocalOnly(t *testing.T) {
	// An ABSENT CIX_BIND_ADDR is the server's all-interfaces default, not
	// loopback. Reading it the other way would show a reassuring "local only"
	// on exactly the installs that are exposed.
	tests := []struct {
		vars map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"CIX_BIND_ADDR": ""}, false},
		{map[string]string{"CIX_BIND_ADDR": "0.0.0.0"}, false},
		{map[string]string{"CIX_BIND_ADDR": "192.168.1.5"}, false},
		{map[string]string{"CIX_BIND_ADDR": "127.0.0.1"}, true},
		{map[string]string{"CIX_BIND_ADDR": " 127.0.0.1 "}, true},
		{map[string]string{"CIX_BIND_ADDR": "::1"}, true},
		{map[string]string{"CIX_BIND_ADDR": "localhost"}, true},
	}
	for _, tc := range tests {
		if got := isLocalOnly(tc.vars); got != tc.want {
			t.Errorf("isLocalOnly(%v) = %v, want %v", tc.vars, got, tc.want)
		}
	}
}

func TestParseTemporaryPassword(t *testing.T) {
	// The real success output. The password line is conditional — it is only
	// printed when the password was generated rather than piped in — and the
	// DISABLED warning is conditional too, so matching the prefix rather than a
	// line index is what keeps this working when the surrounding lines change.
	out := "Password reset for admin@example.com (admin).\n" +
		"Temporary password: mKq7RtVbn3XpZs4Ldy8W\n" +
		"All sessions for this account were revoked; the next login forces a password change.\n"
	pw, ok := parseTemporaryPassword(out)
	if !ok || pw != "mKq7RtVbn3XpZs4Ldy8W" {
		t.Errorf("parseTemporaryPassword() = %q, %v; want the generated password", pw, ok)
	}

	// With a DISABLED account the command appends a warning after the password.
	withWarning := out + "WARNING: this account is DISABLED — it still cannot log in.\n"
	if pw, ok := parseTemporaryPassword(withWarning); !ok || pw != "mKq7RtVbn3XpZs4Ldy8W" {
		t.Errorf("parseTemporaryPassword() with trailing warning = %q, %v", pw, ok)
	}

	// A piped-in password produces no line at all. Reporting some other line as
	// the password would be worse than reporting nothing.
	piped := "Password reset for admin@example.com (admin).\n" +
		"All sessions for this account were revoked; the next login forces a password change.\n"
	if pw, ok := parseTemporaryPassword(piped); ok {
		t.Errorf("parseTemporaryPassword() = %q, true; want no match when none was generated", pw)
	}
	if _, ok := parseTemporaryPassword(""); ok {
		t.Error("parseTemporaryPassword(\"\") reported a match")
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Nothing recorded yet: the question has not been asked.
	if got := loadPrefs(); got.Takeover != "" {
		t.Errorf("loadPrefs() on a fresh home = %+v, want zero", got)
	}
	if err := savePrefs(prefs{Takeover: takeoverKeep}); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	if got := loadPrefs(); got.Takeover != takeoverKeep {
		t.Errorf("loadPrefs() = %+v, want Takeover=%q", got, takeoverKeep)
	}

	// A corrupt file must not stop the app starting; the worst case is being
	// asked the question again.
	path, err := prefsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(); got.Takeover != "" {
		t.Errorf("loadPrefs() on corrupt file = %+v, want zero", got)
	}
}

func TestReadEnvFileDialects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// install-server.sh writes a file this parser also has to read during a
	// takeover, and it does not use our quoting.
	body := "# comment\n\nCIX_PORT=21847\nCIX_API_KEY=\"cix_quoted\"\nexport CIX_DATA_DIR='/tmp/data'\nBAD_LINE\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile: %v", err)
	}
	want := map[string]string{
		"CIX_PORT":     "21847",
		"CIX_API_KEY":  "cix_quoted",
		"CIX_DATA_DIR": "/tmp/data",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("readEnvFile()[%q] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["BAD_LINE"]; ok {
		t.Error("a line without = should be skipped, not stored")
	}
}

func TestIsUserCancelled(t *testing.T) {
	// The message is localised and the spelling differs between locales — this
	// project's own development Mac runs en-GB and emits "cancelled", while the
	// American spelling has one L. Matching either sentence is wrong somewhere,
	// and was: every dialog dismissal in the app was being reported as a
	// failure. The OSStatus is the same number in every language.
	cancelled := []string{
		`0:778: execution error: User cancelled. (-128)`,
		`0:112: execution error: User canceled. (-128)`,
		`0:50: execution error: User canceled the operation. (-128)`,
	}
	for _, s := range cancelled {
		if !isUserCancelled(s) {
			t.Errorf("isUserCancelled(%q) = false, want true", s)
		}
	}

	notCancelled := []string{
		"",
		`0:10: syntax error: Expected end of line but found identifier. (-2741)`,
		`execution error: Finder got an error: Can't get disk "cix". (-1728)`,
	}
	for _, s := range notCancelled {
		if isUserCancelled(s) {
			t.Errorf("isUserCancelled(%q) = true, want false", s)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	// The release workflow generates this with `shasum -a 256 ./*.dmg`, which
	// records a leading "./" — a plain equality check on the filename would
	// find nothing and abort every update.
	sums := "" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  ./cix-0.3.1-arm64.dmg\n" +
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08  ./checksums-other.txt\n"

	got, err := checksumFor(sums, "cix-0.3.1-arm64.dmg")
	if err != nil {
		t.Fatalf("checksumFor: %v", err)
	}
	if got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("checksumFor() = %q", got)
	}

	// A name that is not listed must be an error, never a silent pass: the
	// checksum is the only integrity check an unnotarised download gets.
	if _, err := checksumFor(sums, "cix-9.9.9-arm64.dmg"); err == nil {
		t.Error("checksumFor() on a missing entry returned no error")
	}
	if _, err := checksumFor("", "anything.dmg"); err == nil {
		t.Error("checksumFor() on empty input returned no error")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "cix-0.3.1-arm64.dmg")
	if err := os.WriteFile(payload, []byte("pretend disk image"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("pretend disk image"))
	sums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(sums, fmt.Appendf(nil, "%x  ./cix-0.3.1-arm64.dmg\n", sum), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(payload, sums, "cix-0.3.1-arm64.dmg"); err != nil {
		t.Errorf("verifyChecksum on a matching file = %v, want nil", err)
	}

	// A truncated or tampered download must not install.
	if err := os.WriteFile(payload, []byte("pretend disk imag"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(payload, sums, "cix-0.3.1-arm64.dmg"); err == nil {
		t.Error("verifyChecksum accepted a file whose contents changed")
	}
}

func TestParseMountPoint(t *testing.T) {
	// hdiutil prints one line per partition; the volume is on the last one.
	out := "/dev/disk4          \tGUID_partition_scheme          \t\n" +
		"/dev/disk4s1        \tApple_HFS                      \t/Volumes/cix\n"
	if got, want := parseMountPoint(out), "/Volumes/cix"; got != want {
		t.Errorf("parseMountPoint() = %q, want %q", got, want)
	}
	if got := parseMountPoint("/dev/disk9\tGUID_partition_scheme\t\n"); got != "" {
		t.Errorf("parseMountPoint() with no volume = %q, want empty", got)
	}
}

func TestCheckWritable(t *testing.T) {
	if err := checkWritable(t.TempDir()); err != nil {
		t.Errorf("checkWritable on a temp dir = %v, want nil", err)
	}
	// The point of the preflight: find out before spending a download, while
	// the installed app is still untouched and the advice can be "drag the new
	// one over the old one".
	if err := checkWritable("/usr/bin"); err == nil {
		t.Skip("running as root, or /usr/bin is writable — preflight cannot be exercised")
	}
}
