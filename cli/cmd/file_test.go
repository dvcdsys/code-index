package cmd

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// captureStdoutStderr runs fn with both os.Stdout and os.Stderr redirected,
// returning what was written to each. The file command sends content to stdout
// and the truncation warning to stderr, so a test must see both streams.
func captureStdoutStderr(fn func() error) (stdout, stderr string, err error) {
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	err = fn()

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr

	var bufOut, bufErr bytes.Buffer
	_, _ = io.Copy(&bufOut, rOut)
	_, _ = io.Copy(&bufErr, rErr)
	return bufOut.String(), bufErr.String(), err
}

// fileTestServer serves the project list plus one FileContent response.
func fileTestServer(t *testing.T, hash string, fc map[string]any) {
	t.Helper()
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/projects"):
			writeJSON(w, 200, map[string]any{"projects": []any{}, "total": 0})
		case strings.Contains(r.URL.Path, hash+"/file"):
			writeJSON(w, 200, fc)
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)
}

func setFileFlags(t *testing.T, project string, raw bool, lines string) {
	t.Helper()
	oldProj, oldName, oldLines, oldRaw := fileProject, fileName, fileLines, fileRaw
	t.Cleanup(func() { fileProject, fileName, fileLines, fileRaw = oldProj, oldName, oldLines, oldRaw })
	fileProject, fileName, fileLines, fileRaw = project, "", lines, raw
}

// TestRunFile_RawTruncationWarning is the finding-8 guard: in --raw mode the
// truncation warning must still reach stderr (so a `cix file … --raw > copy`
// pipe doesn't silently drop content), while stdout stays clean raw content.
func TestRunFile_RawTruncationWarning(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)
	fileTestServer(t, hash, map[string]any{
		"file_path":   "big.txt",
		"start_line":  1,
		"end_line":    3,
		"total_lines": 5000,
		"truncated":   true,
		"content":     "L1\nL2\nL3",
	})
	setFileFlags(t, proj, true, "")

	stdout, stderr, err := captureStdoutStderr(func() error {
		return runFile(nil, []string{"big.txt"})
	})
	if err != nil {
		t.Fatalf("runFile: %v", err)
	}
	if !strings.Contains(stdout, "L1\nL2\nL3") {
		t.Errorf("stdout = %q, want the raw content", stdout)
	}
	if strings.Contains(stdout, "truncated") {
		t.Errorf("stdout must not carry the warning (it pollutes a piped copy): %q", stdout)
	}
	if !strings.Contains(stderr, "truncated") {
		t.Errorf("stderr = %q, want a truncation warning even in --raw mode", stderr)
	}
}

// TestRunFile_RawNoWarningWhenComplete confirms a non-truncated raw read prints
// nothing to stderr.
func TestRunFile_RawNoWarningWhenComplete(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)
	fileTestServer(t, hash, map[string]any{
		"file_path":   "small.txt",
		"start_line":  1,
		"end_line":    2,
		"total_lines": 2,
		"truncated":   false,
		"content":     "L1\nL2",
	})
	setFileFlags(t, proj, true, "")

	stdout, stderr, err := captureStdoutStderr(func() error {
		return runFile(nil, []string{"small.txt"})
	})
	if err != nil {
		t.Fatalf("runFile: %v", err)
	}
	if !strings.Contains(stdout, "L1\nL2") {
		t.Errorf("stdout = %q, want the raw content", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("stderr = %q, want empty for a complete file", stderr)
	}
}

// TestRunFile_NumberedTruncationWarning confirms the numbered (non-raw) path
// still warns — the shared warning block must fire in both modes.
func TestRunFile_NumberedTruncationWarning(t *testing.T) {
	proj := t.TempDir()
	hash := projectHash(proj)
	fileTestServer(t, hash, map[string]any{
		"file_path":   "big.txt",
		"start_line":  1,
		"end_line":    3,
		"total_lines": 5000,
		"truncated":   true,
		"content":     "L1\nL2\nL3",
	})
	setFileFlags(t, proj, false, "")

	stdout, stderr, err := captureStdoutStderr(func() error {
		return runFile(nil, []string{"big.txt"})
	})
	if err != nil {
		t.Fatalf("runFile: %v", err)
	}
	if !strings.Contains(stdout, "1  L1") {
		t.Errorf("stdout = %q, want numbered lines", stdout)
	}
	if !strings.Contains(stderr, "truncated") {
		t.Errorf("stderr = %q, want a truncation warning", stderr)
	}
}

// TestParseLineRange_Inverted covers the CLI-side guard for finding 4: an
// inverted --lines range is rejected before hitting the server.
func TestParseLineRange_Inverted(t *testing.T) {
	if _, _, err := parseLineRange("4:2"); err == nil {
		t.Error("parseLineRange(\"4:2\") = nil error, want a rejection of end < start")
	}
	// A valid range still parses.
	if s, e, err := parseLineRange("2:4"); err != nil || s != 2 || e != 4 {
		t.Errorf("parseLineRange(\"2:4\") = (%d,%d,%v), want (2,4,nil)", s, e, err)
	}
}
