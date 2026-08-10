package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestSnapshotLines(t *testing.T) {
	running := snapshot{
		State:   stateRunning,
		Port:    21847,
		Managed: true,
		Status: &client.StatusResponse{
			EmbeddingProvider: "ollama",
			EmbeddingModel:    "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF",
		},
		EmbeddingsOK: true,
	}

	if got, want := running.ServerLine(), "cix-server: Running (:21847)"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
	// Readiness is on the dot, not in the text: the words "— ready" cost menu
	// width, and an NSMenu is as wide as its widest row.
	if got, want := running.EmbeddingsLine(), "Embeddings: llama.cpp (bundled)"; got != want {
		t.Errorf("EmbeddingsLine() = %q, want %q", got, want)
	}
	// The provider prefix is stripped — the row above already names the
	// provider, and repeating the misleading kind defeats providerLabel — and
	// the remainder is middle-truncated to the width cap.
	if got, want := running.ModelLine(), "Model: awhiteside/Co…bed-Q8_0-GGUF"; got != want {
		t.Errorf("ModelLine() = %q, want %q", got, want)
	}
	if got, want := running.ModelName(), "awhiteside/CodeRankEmbed-Q8_0-GGUF"; got != want {
		t.Errorf("ModelName() = %q, want %q (the details submenu keeps the full id)", got, want)
	}
}

func TestSnapshotLines_NotRunning(t *testing.T) {
	// A cold start loads the embedding model in silence for up to a few
	// minutes. Calling that "Stopped" is what makes people kill and restart a
	// server that was nearly ready, so it gets its own state.
	starting := snapshot{State: stateStarting, Managed: true}
	if got, want := starting.ServerLine(), "cix-server: Starting…"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
	// Provider details from a server that is not answering are stale by
	// definition, so no row claims otherwise.
	if got, want := starting.ModelLine(), ""; got != want {
		t.Errorf("ModelLine() = %q, want %q (hidden)", got, want)
	}
	if got, want := starting.EmbeddingsLine(), "Embeddings: unknown"; got != want {
		t.Errorf("EmbeddingsLine() = %q, want %q", got, want)
	}

	// An agent installed by install-server.sh owns the same launchd label. The
	// app observes it but must not offer to drive it. Abbreviated in the row
	// because spelled out it is 40 characters and would set the menu's width on
	// its own; the details submenu carries the explanation.
	external := snapshot{State: stateStopped, Managed: false}
	if got, want := external.ServerLine(), "cix-server: Stopped (external)"; got != want {
		t.Errorf("ServerLine() = %q, want %q", got, want)
	}
	// A disabled Start/Stop is otherwise inexplicable, so the reason is in the
	// details submenu.
	if !strings.Contains(external.DetailManaged(), "install-server.sh") {
		t.Errorf("DetailManaged() should name the external installer, got %q", external.DetailManaged())
	}
}

func TestDetailRows(t *testing.T) {
	// The details submenu replaced menu-item tooltips: once any AppKit tooltip
	// has shown, every later one appears with no delay, and they are positioned
	// against the item rather than the pointer. Neither has an API.
	s := snapshot{
		State: stateRunning, PID: 4242, Port: 21847, Managed: true, LocalOnly: true,
		Status: &client.StatusResponse{
			ServerVersion:  "0.12.4",
			EmbeddingModel: "ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF",
		},
	}
	if got, want := s.DetailProcess(), "Process: 4242"; got != want {
		t.Errorf("DetailProcess() = %q, want %q", got, want)
	}
	if got, want := s.DetailPort(), "Port: 21847"; got != want {
		t.Errorf("DetailPort() = %q, want %q", got, want)
	}
	// "127.0.0.1" is precise and means nothing to most people; the exposure is
	// what they need to know.
	if got, want := s.DetailNetwork(), "Network: this Mac only"; got != want {
		t.Errorf("DetailNetwork() = %q, want %q", got, want)
	}
	// The full id, which the truncated row could not carry — the whole reason
	// this submenu exists.
	if got, want := s.DetailModel(), "Model: awhiteside/CodeRankEmbed-Q8_0-GGUF"; got != want {
		t.Errorf("DetailModel() = %q, want %q", got, want)
	}
	if got, want := s.DetailVersion(), "Server 0.12.4"; got != want {
		t.Errorf("DetailVersion() = %q, want %q", got, want)
	}
	if got := s.DetailManaged(); got != "" {
		t.Errorf("DetailManaged() = %q, want empty for an app-managed agent", got)
	}

	// Rows with nothing to say are hidden, never blank.
	stopped := snapshot{State: stateStopped, Port: 21847, Managed: true}
	if got, want := stopped.DetailProcess(), "Process: not running"; got != want {
		t.Errorf("DetailProcess() = %q, want %q", got, want)
	}
	if stopped.DetailModel() != "" || stopped.DetailVersion() != "" {
		t.Error("model and version rows should be empty when the server is not answering")
	}
	exposed := snapshot{State: stateRunning, Port: 21847, Managed: true, LocalOnly: false}
	if got, want := exposed.DetailNetwork(), "Network: reachable from your network"; got != want {
		t.Errorf("DetailNetwork() = %q, want %q", got, want)
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

func TestEllipsize(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		// Middle, not tail: both ends of a qualified name carry information,
		// and tail truncation renders every Hugging Face model as its owner.
		{"awhiteside/CodeRankEmbed-Q8_0-GGUF", 27, "awhiteside/Co…d-Q8_0-GGUF"},
		{"abcdefghij", 5, "abc…j"},
		// Multi-byte input must be cut on rune boundaries, not bytes.
		{"привіт-світе-довгий-рядок", 10, "привіт…ядок"},
	}
	for _, tc := range tests {
		got := ellipsize(tc.in, tc.max)
		if len([]rune(got)) > tc.max && len([]rune(tc.in)) > tc.max {
			t.Errorf("ellipsize(%q, %d) = %q, %d runes — over the cap", tc.in, tc.max, got, len([]rune(got)))
		}
		if !utf8.ValidString(got) {
			t.Errorf("ellipsize(%q, %d) produced invalid UTF-8: %q", tc.in, tc.max, got)
		}
	}
}

func TestRowsFitTheWidthCap(t *testing.T) {
	// An NSMenu is exactly as wide as its widest row, so a long model id used
	// to stretch the whole menu. Every row must stay inside the cap.
	s := snapshot{
		State:   stateRunning,
		Port:    21847,
		Managed: true,
		Status: &client.StatusResponse{
			EmbeddingProvider: "ollama",
			EmbeddingModel:    "ollama:some-extremely-long-organisation/an-even-longer-model-name-v2",
		},
		EmbeddingsOK: true,
	}
	for name, line := range map[string]string{
		"ServerLine":     s.ServerLine(),
		"EmbeddingsLine": s.EmbeddingsLine(),
		"ModelLine":      s.ModelLine(),
	} {
		if n := len([]rune(line)); n > maxRowRunes {
			t.Errorf("%s = %q is %d runes, over the %d cap", name, line, n, maxRowRunes)
		}
	}
	// The untruncated value is still available for the details submenu.
	if got, want := s.ModelName(), "some-extremely-long-organisation/an-even-longer-model-name-v2"; got != want {
		t.Errorf("ModelName() = %q, want %q", got, want)
	}
}

func TestDots(t *testing.T) {
	running := snapshot{State: stateRunning, Status: &client.StatusResponse{}, EmbeddingsOK: true}
	if running.ServerDot() != dotGreen || running.EmbeddingsDot() != dotGreen {
		t.Error("a running server with a ready provider should be green on both rows")
	}
	// Starting is amber, not red: a cold start loading a model is working, and
	// red is what makes people kill it.
	if (snapshot{State: stateStarting}).ServerDot() != dotAmber {
		t.Error("starting should be amber")
	}
	if (snapshot{State: stateStopped}).ServerDot() != dotRed {
		t.Error("stopped should be red")
	}
	// Nothing is known about the provider when the server is down; grey says
	// "unknown", red would claim it is broken.
	if (snapshot{State: stateStopped}).EmbeddingsDot() != dotGrey {
		t.Error("provider state should be grey when the server is not answering")
	}
	if len(dotPNG(dotGreen)) == 0 {
		t.Error("dotPNG returned no bytes")
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
