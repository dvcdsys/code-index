package cmd

import (
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/client"
)

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

// TestFormatFileContent_Normal is the MCP rendering path for cix_file: a header
// with the line window + total, then tab-numbered lines the model can cite.
func TestFormatFileContent_Normal(t *testing.T) {
	out := formatFileContent(&client.FileContent{
		FilePath:   "internal/x.go",
		Language:   strptr("go"),
		StartLine:  2,
		EndLine:    3,
		TotalLines: 10,
		Content:    "b\nc",
	})
	if !strings.Contains(out, "internal/x.go (lines 2–3 of 10 · go)") {
		t.Errorf("missing/incorrect header:\n%s", out)
	}
	if !strings.Contains(out, "2\tb") || !strings.Contains(out, "3\tc") {
		t.Errorf("lines not numbered from start_line:\n%s", out)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("must not report truncation when Truncated=false:\n%s", out)
	}
}

// TestFormatFileContent_Truncated confirms the server's truncated flag surfaces
// to the model.
func TestFormatFileContent_Truncated(t *testing.T) {
	out := formatFileContent(&client.FileContent{
		FilePath:   "big.txt",
		StartLine:  1,
		EndLine:    2,
		TotalLines: 9000,
		Truncated:  true,
		Content:    "L1\nL2",
	})
	if !strings.Contains(out, "of 9000") {
		t.Errorf("header should report the true total_lines:\n%s", out)
	}
	if !strings.Contains(out, "truncated by server limits") {
		t.Errorf("truncation notice missing:\n%s", out)
	}
}

// TestFormatFileContent_EmptyRange guards the empty-range protocol: the server
// signals "no lines" with end_line < start_line, and the MCP output must say so
// rather than printing a spurious blank line.
func TestFormatFileContent_EmptyRange(t *testing.T) {
	out := formatFileContent(&client.FileContent{
		FilePath:   "f.txt",
		StartLine:  99,
		EndLine:    98, // end < start → empty
		TotalLines: 5,
		Content:    "",
	})
	if !strings.Contains(out, "(no lines in range)") {
		t.Errorf("empty range not signalled:\n%s", out)
	}
	if strings.Contains(out, "99\t") {
		t.Errorf("must not number a line for an empty range:\n%s", out)
	}
}

// TestFormatTree_Entries covers the cix_tree rendering: dirs with a trailing
// slash, files with a byte size, and the truncation notice.
func TestFormatTree_Entries(t *testing.T) {
	out := formatTree(&client.DirectoryListing{
		Dir: "internal",
		Entries: []client.TreeEntry{
			{Name: "sub", Type: "dir"},
			{Name: "x.go", Type: "file", Size: intptr(42), Language: strptr("go")},
		},
		Truncated: true,
	})
	if !strings.Contains(out, "internal/ (2 entries)") {
		t.Errorf("header wrong:\n%s", out)
	}
	if !strings.Contains(out, "  sub/") {
		t.Errorf("dir should render with trailing slash:\n%s", out)
	}
	if !strings.Contains(out, "  x.go  (42 B)") {
		t.Errorf("file should render with byte size:\n%s", out)
	}
	if !strings.Contains(out, "truncated — more entries than the listing cap") {
		t.Errorf("truncation notice missing:\n%s", out)
	}
}

// TestFormatTree_RootEmpty confirms the root dir renders as "." with no entries.
func TestFormatTree_RootEmpty(t *testing.T) {
	out := formatTree(&client.DirectoryListing{Dir: "", Entries: nil})
	if !strings.Contains(out, "./ (0 entries)") {
		t.Errorf("root/empty listing wrong:\n%s", out)
	}
}
