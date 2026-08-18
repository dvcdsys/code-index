package chunker

import (
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/tokenizer"
)

// fakeBudget counts one token per whitespace-separated word and cuts on word
// boundaries. Deterministic and independent of any vocabulary, so these tests
// assert the CHUNKER's behaviour rather than a tokenizer's — the real
// tokenizer has its own tests.
type fakeBudget struct{ maxInput int }

func (f fakeBudget) MaxInputTokens() int { return f.maxInput }
func (f fakeBudget) ExactCounts() bool   { return true }

func (f fakeBudget) CountTokens(s string) int { return len(strings.Fields(s)) }

func (f fakeBudget) SplitPoints(s string, budget int) ([]int, int) {
	var offsets []int
	count, since := 0, 0
	inWord := false
	for i := 0; i < len(s); i++ {
		isSpace := s[i] == ' ' || s[i] == '\t' || s[i] == '\n'
		if !isSpace && !inWord {
			inWord = true
			count++
			since++
			if since > budget {
				offsets = append(offsets, i)
				since = 1
			}
		} else if isSpace {
			inWord = false
		}
	}
	return offsets, count
}

var _ tokenizer.Budget = fakeBudget{}

// TestTokenBudgetBoundsEveryChunk is the property the integration exists for:
// with a budget in hand, no emitted chunk may exceed it.
func TestTokenBudgetBoundsEveryChunk(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("func run() {\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("\tdo something with several words on this line\n")
	}
	sb.WriteString("}\n")

	b := fakeBudget{maxInput: 4096}
	chunks, _, err := ChunkFileTokens("x.go", sb.String(), "go", 0, b, 50)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the body to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if n := b.CountTokens(c.Content); n > 50 {
			t.Errorf("chunk %d is %d tokens, over budget 50", i, n)
		}
	}
}

// TestLongSingleLineIsSplit covers the hole the byte splitter had: its loop
// requires more than one line, so a minified file arrived at the embedder as
// one enormous chunk. On the reference corpus that produced a 65 KB chunk
// whose vector was an average of byte windows.
func TestLongSingleLineIsSplit(t *testing.T) {
	line := strings.TrimSpace(strings.Repeat("token ", 5000))
	b := fakeBudget{maxInput: 4096}

	chunks, _, err := ChunkFileTokens("min.js", line, "javascript", 0, b, 100)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	for i, c := range chunks {
		if n := b.CountTokens(c.Content); n > 100 {
			t.Errorf("chunk %d is %d tokens, over budget 100 — long line not cut", i, n)
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the single line to be cut, got %d chunk(s)", len(chunks))
	}
}

// TestBudgetCappedByModelContext — a chunk target above the model's own input
// window is meaningless; the smaller of the two must win.
func TestBudgetCappedByModelContext(t *testing.T) {
	src := strings.TrimSpace(strings.Repeat("word ", 400))
	b := fakeBudget{maxInput: 40}

	chunks, _, err := ChunkFileTokens("x.txt", src, "text", 0, b, 10000)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	for i, c := range chunks {
		if n := b.CountTokens(c.Content); n > 40 {
			t.Errorf("chunk %d is %d tokens, over the model's %d-token window", i, n, 40)
		}
	}
}

// TestEstimatingBudgetKeepsBytePath — a provider without a real tokenizer must
// not be routed through the token splitter: its numbers are the same guess the
// byte path already makes, and pretending otherwise hides that from the caller.
func TestEstimatingBudgetKeepsBytePath(t *testing.T) {
	src := strings.Repeat("x := 1\n", 2000)
	got, _, err := ChunkFileTokens("x.go", src, "go", 0, estimatingBudget{}, 10)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	want, _, err := ChunkFile("x.go", src, "go", 0)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("estimating budget changed chunking: %d chunks vs %d on the byte path",
			len(got), len(want))
	}
}

type estimatingBudget struct{ fakeBudget }

func (estimatingBudget) ExactCounts() bool { return false }

// TestNilBudgetUnchanged pins that the default path is untouched.
func TestNilBudgetUnchanged(t *testing.T) {
	src := strings.Repeat("func f() { return 1 }\n", 500)
	a, _, err := ChunkFileTokens("x.go", src, "go", 0, nil, 0)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	b, _, err := ChunkFile("x.go", src, "go", 0)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if len(a) != len(b) {
		t.Errorf("nil budget diverged from ChunkFile: %d vs %d chunks", len(a), len(b))
	}
}
