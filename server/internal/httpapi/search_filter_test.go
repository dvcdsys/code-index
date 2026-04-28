package httpapi

import (
	"testing"

	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

func mkRawResult(path string, score float32) vectorStoreResult {
	return vectorStoreResult{
		r: vectorstore.SearchResult{
			FilePath:  path,
			StartLine: 1,
			EndLine:   10,
			Content:   "stub",
			Score:     score,
			Language:  "go",
		},
	}
}

func TestFilterToSearchItems_ExcludesPrefixDropsMatchingPaths(t *testing.T) {
	raw := []vectorStoreResult{
		mkRawResult("/proj/server/cmd/cix-server/main.go", 0.9),
		mkRawResult("/proj/bench/fixtures/sample.py", 0.85),
		mkRawResult("/proj/legacy/python-api/scripts/profile_vram.py", 0.8),
		mkRawResult("/proj/cli/main.go", 0.7),
	}

	out := filterToSearchItems(raw, 0.0, nil, []string{"/proj/bench", "/proj/legacy"}, nil, false)
	if len(out) != 2 {
		t.Fatalf("want 2 results after exclude, got %d", len(out))
	}
	for _, r := range out {
		if r.FilePath == "/proj/bench/fixtures/sample.py" || r.FilePath == "/proj/legacy/python-api/scripts/profile_vram.py" {
			t.Errorf("excluded path leaked through: %s", r.FilePath)
		}
	}
}

func TestFilterToSearchItems_ExcludesSubstringMatch(t *testing.T) {
	// Substring match parity with --in: an exclude of "fixtures" drops any
	// path that contains the substring, not just a prefix match.
	raw := []vectorStoreResult{
		mkRawResult("/proj/server/cmd/cix-server/main.go", 0.9),
		mkRawResult("/proj/bench/fixtures/sample.py", 0.85),
	}
	out := filterToSearchItems(raw, 0.0, nil, []string{"fixtures"}, nil, false)
	if len(out) != 1 {
		t.Fatalf("want 1 after substring exclude, got %d", len(out))
	}
	if out[0].FilePath != "/proj/server/cmd/cix-server/main.go" {
		t.Errorf("unexpected survivor: %s", out[0].FilePath)
	}
}

func TestFilterToSearchItems_ExcludesAndPathsCombined(t *testing.T) {
	// --in narrows to a directory; --exclude further trims a subdirectory.
	raw := []vectorStoreResult{
		mkRawResult("/proj/server/internal/httpapi/search.go", 0.9),
		mkRawResult("/proj/server/internal/httpapi/search_test.go", 0.85),
		mkRawResult("/proj/cli/cmd/search.go", 0.8),
	}
	out := filterToSearchItems(raw, 0.0,
		[]string{"/proj/server"},
		[]string{"_test.go"},
		nil, false)
	if len(out) != 1 {
		t.Fatalf("want 1 after path+exclude, got %d", len(out))
	}
	if out[0].FilePath != "/proj/server/internal/httpapi/search.go" {
		t.Errorf("unexpected survivor: %s", out[0].FilePath)
	}
}

func TestFilterToSearchItems_NilExcludesIsNoop(t *testing.T) {
	raw := []vectorStoreResult{
		mkRawResult("/a.go", 0.9),
		mkRawResult("/b.go", 0.8),
	}
	out := filterToSearchItems(raw, 0.0, nil, nil, nil, false)
	if len(out) != 2 {
		t.Errorf("nil excludes must not drop anything; got %d", len(out))
	}
}
