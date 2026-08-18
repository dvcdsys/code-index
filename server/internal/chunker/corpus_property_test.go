package chunker

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/tokenizer"
	"github.com/dvcdsys/code-index/server/internal/tokenizer/bpecount"
)

// Property test over a real corpus.
//
// Chunk splitting under a token budget has no external oracle: unlike the
// tokenizer, which can be checked against HuggingFace's implementation and
// against Voyage's own billing, "where should a chunk be cut" is our decision
// and there is nothing to compare it to. What can be checked is that the
// properties we chose actually hold on inputs we did not write — and the
// bugs this file exists to catch were all found by real files rather than by
// hand-written cases:
//
//   - a 65 KB single line (a Zig integer literal) that the byte splitter left
//     whole, at 65,553 tokens against a 32K context;
//   - minified JavaScript, which has no grammar and therefore reaches the
//     sliding-window fallback rather than the tree-sitter path;
//   - per-line token counting, which was 3% under the truth because joining
//     lines reinserts newlines that cost tokens.
//
// The corpus is not in the repository — it is a local fixture of cloned
// repositories, tens of gigabytes. Point the test at one:
//
//	CIX_TEST_CORPUS_DIR=…/loadtests/data/repos/repos \
//	CIX_TEST_TOKENIZER=…/voyage-code-3.tokenizer.json \
//	go test ./internal/chunker/ -run Corpus
//
// Without those it skips, so a clean checkout and CI stay green.

type realBudget struct{ tk *bpecount.Counter }

func (realBudget) MaxInputTokens() int                        { return 32000 }
func (realBudget) ExactCounts() bool                          { return true }
func (b realBudget) CountTokens(s string) int                 { return b.tk.Count(s) }
func (b realBudget) SplitPoints(s string, n int) ([]int, int) { return b.tk.SplitPoints(s, n) }

var _ tokenizer.Budget = realBudget{}

var extLang = map[string]string{
	".go": "go", ".py": "python", ".ts": "typescript", ".tsx": "tsx",
	".js": "javascript", ".jsx": "javascript", ".java": "java", ".rs": "rust",
	".c": "c", ".h": "c", ".cpp": "cpp", ".rb": "ruby", ".php": "php",
	".kt": "kotlin", ".swift": "swift", ".ex": "elixir", ".zig": "zig",
	".lua": "lua", ".sh": "bash", ".md": "markdown", ".json": "json",
}

// sampleCorpus walks the fixture and returns up to n files, deterministically
// shuffled so a failure is reproducible.
func sampleCorpus(t *testing.T, root string, n int) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is not this test's problem
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := extLang[strings.ToLower(filepath.Ext(path))]; ok {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	sort.Strings(files)
	rng := rand.New(rand.NewSource(20260818))
	rng.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
	if len(files) > n {
		files = files[:n]
	}
	return files
}

func corpusBudget(t *testing.T) (realBudget, string) {
	t.Helper()
	dir := os.Getenv("CIX_TEST_CORPUS_DIR")
	tok := os.Getenv("CIX_TEST_TOKENIZER")
	if dir == "" || tok == "" {
		t.Skip("set CIX_TEST_CORPUS_DIR and CIX_TEST_TOKENIZER to run corpus property tests")
	}
	tk, err := bpecount.Load(tok)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	return realBudget{tk}, dir
}

// TestCorpusChunksRespectBudget is the property that matters to the API: no
// chunk may cost more tokens than the budget, whatever path produced it.
func TestCorpusChunksRespectBudget(t *testing.T) {
	b, dir := corpusBudget(t)
	const budget = 1500

	files := sampleCorpus(t, dir, 400)
	if len(files) == 0 {
		t.Skip("corpus contains no recognised source files")
	}

	var checked, chunks, worst int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil || len(src) == 0 {
			continue
		}
		lang := extLang[strings.ToLower(filepath.Ext(f))]
		got, _, err := ChunkFileTokens(f, string(src), lang, 0, b, budget)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		checked++
		chunks += len(got)
		for i, c := range got {
			n := b.CountTokens(c.Content)
			if n > worst {
				worst = n
			}
			if n > budget {
				t.Errorf("%s chunk %d: %d tokens, over budget %d", f, i, n, budget)
			}
		}
	}
	t.Logf("%d files, %d chunks, largest %d tokens (budget %d)", checked, chunks, worst, budget)
}

// TestCorpusSplitPreservesContent asserts the splitter loses and duplicates
// nothing, on chunks taken from real files rather than constructed ones. Run
// against splitChunkTokens directly: the sliding-window fallback overlaps its
// windows by design, so whole-pipeline output is not expected to concatenate
// back to the source.
func TestCorpusSplitPreservesContent(t *testing.T) {
	b, dir := corpusBudget(t)
	const budget = 300

	var split int
	for _, f := range sampleCorpus(t, dir, 200) {
		src, err := os.ReadFile(f)
		if err != nil || len(src) == 0 {
			continue
		}
		whole := Chunk{
			Content:   string(src),
			FilePath:  f,
			StartLine: 1,
			ChunkType: "file",
		}
		if b.CountTokens(whole.Content) <= budget {
			continue
		}
		pieces := splitChunkTokens(whole, b, budget)
		split++

		var rebuilt strings.Builder
		for i, p := range pieces {
			rebuilt.WriteString(p.Content)
			if n := b.CountTokens(p.Content); n > budget {
				t.Errorf("%s piece %d: %d tokens, over budget %d", f, i, n, budget)
			}
		}
		if rebuilt.String() != whole.Content {
			t.Errorf("%s: pieces do not reconstruct the file (%d bytes vs %d)",
				f, rebuilt.Len(), len(whole.Content))
		}
	}
	t.Logf("%d files exceeded the budget and were split", split)
}

// TestCorpusLineNumbers — a chunk's StartLine must point at the line its text
// actually begins on, or search results send the reader to the wrong place.
func TestCorpusLineNumbers(t *testing.T) {
	b, dir := corpusBudget(t)
	const budget = 300

	for _, f := range sampleCorpus(t, dir, 150) {
		src, err := os.ReadFile(f)
		if err != nil || len(src) == 0 {
			continue
		}
		whole := Chunk{Content: string(src), FilePath: f, StartLine: 1}
		if b.CountTokens(whole.Content) <= budget {
			continue
		}
		line := 1
		for i, p := range splitChunkTokens(whole, b, budget) {
			if p.StartLine != line {
				t.Errorf("%s piece %d starts at line %d, expected %d", f, i, p.StartLine, line)
				break
			}
			line += strings.Count(p.Content, "\n")
		}
	}
}
