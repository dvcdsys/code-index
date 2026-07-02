package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// The goldens below pin the exact wire JSON of the file/tree endpoints. The CLI
// and its built-in MCP live in a SEPARATE module (github.com/anthropics/
// code-index/cli) that cannot import this one, so the contract is held by twin
// fixtures: an identical golden lives in cli/internal/client/file_test.go
// (wantFileContentWire / wantDirectoryListingWire). If a field is renamed,
// retyped, or reordered on either side, one of these tests fails — forcing the
// other module to be updated in the same PR (the CLI/server "update both sides"
// rule). Field ORDER matters here because encoding/json emits struct fields in
// declaration order; the CLI only decodes, so order is irrelevant there, but we
// keep the bytes identical to make the twin explicit.

const (
	wantFileContentWire      = `{"content":"L1\nL2","end_line":2,"file_path":"internal/x.go","language":"go","start_line":1,"total_lines":2,"truncated":false}`
	wantDirectoryListingWire = `{"dir":"internal","entries":[{"name":"sub","type":"dir"},{"language":"go","name":"x.go","size":42,"type":"file"}],"truncated":false}`
)

func TestFileContent_WireContract(t *testing.T) {
	lang := "go"
	b, err := json.Marshal(openapi.FileContent{
		Content:    "L1\nL2",
		EndLine:    2,
		FilePath:   "internal/x.go",
		Language:   &lang,
		StartLine:  1,
		TotalLines: 2,
		Truncated:  false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != wantFileContentWire {
		t.Errorf("FileContent wire shape drifted from the CLI contract:\n got: %s\nwant: %s\n"+
			"→ update cli/internal/client/file.go and its twin fixture in the same PR.", b, wantFileContentWire)
	}
}

func TestDirectoryListing_WireContract(t *testing.T) {
	lang := "go"
	size := 42
	b, err := json.Marshal(openapi.DirectoryListing{
		Dir: "internal",
		Entries: []openapi.TreeEntry{
			{Name: "sub", Type: openapi.Dir},
			{Name: "x.go", Type: openapi.File, Size: &size, Language: &lang},
		},
		Truncated: false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != wantDirectoryListingWire {
		t.Errorf("DirectoryListing wire shape drifted from the CLI contract:\n got: %s\nwant: %s\n"+
			"→ update cli/internal/client/file.go and its twin fixture in the same PR.", b, wantDirectoryListingWire)
	}
}
