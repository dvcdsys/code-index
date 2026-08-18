package chunker

import (
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/tokenizer"
)

// fakeBudget is deliberately ADVERSARIAL: a newline costs a token, exactly
// like it does in the real tokenizer, where a line break plus the next line's
// indentation forms its own pre-token.
//
// The first version of this double counted whitespace-separated words and let
// newlines be free. That made the sum of per-line counts equal the count of
// the joined text — which is precisely the assumption the implementation got
// wrong, so the double agreed with the bug and the tests passed while real
// files came out 3% over budget. A test double that cannot express the
// failure mode cannot catch it.
//
// Counting rule: one token per word start, one per newline. Sum over pieces
// therefore does NOT equal the count of the concatenation unless the pieces
// are substrings — which is the property the splitter must have.
type fakeBudget struct{ maxInput int }

func (f fakeBudget) MaxInputTokens() int { return f.maxInput }
func (f fakeBudget) ExactCounts() bool   { return true }

func (f fakeBudget) CountTokens(s string) int {
	n, inWord := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\n':
			n++
			inWord = false
		case c == ' ' || c == '\t' || c == '\r':
			inWord = false
		default:
			if !inWord {
				inWord = true
				n++
			}
		}
	}
	return n
}

// SplitPoints cuts before the token that would overflow the budget, so every
// piece it produces costs at most budget under CountTokens above.
func (f fakeBudget) SplitPoints(s string, budget int) ([]int, int) {
	var offsets []int
	total, since := 0, 0
	inWord := false
	cut := func(at int) {
		offsets = append(offsets, at)
		since = 1
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\n':
			total++
			since++
			if since > budget {
				cut(i)
			}
			inWord = false
		case c == ' ' || c == '\t' || c == '\r':
			inWord = false
		default:
			if !inWord {
				inWord = true
				total++
				since++
				if since > budget {
					cut(i)
				}
			}
		}
	}
	return offsets, total
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

// TestTokenSplitPreservesContent pins the invariant that makes the token
// splitter exact: its pieces are SUBSTRINGS of the chunk it was given, so
// concatenating them reproduces it byte for byte — nothing lost, nothing
// duplicated.
//
// The first implementation instead re-joined lines it had counted separately,
// and the newlines it reinserted cost tokens the running total never saw. A
// 1500-token budget produced 1546-token chunks on real files. Slicing the
// original removes that class of error rather than compensating for it.
//
// Asserted on splitChunkTokens directly, not through ChunkFileTokens: the
// sliding-window fallback deliberately overlaps its windows for recall, so
// whole-pipeline output is not expected to concatenate back.
func TestTokenSplitPreservesContent(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("some line with a handful of words in it\n")
	}
	src := Chunk{
		Content:    sb.String(),
		FilePath:   "x.txt",
		StartLine:  1,
		EndLine:    200,
		ChunkType:  "function",
		SymbolName: strPtr("run"),
	}
	b := fakeBudget{maxInput: 4096}

	pieces := splitChunkTokens(src, b, 40)
	var rebuilt strings.Builder
	for i, c := range pieces {
		rebuilt.WriteString(c.Content)
		if n := b.CountTokens(c.Content); n > 40 {
			t.Errorf("piece %d is %d tokens, over budget 40", i, n)
		}
	}
	if rebuilt.String() != src.Content {
		t.Errorf("concatenated pieces differ from the source (%d bytes vs %d)",
			rebuilt.Len(), len(src.Content))
	}
	if pieces[0].SymbolName == nil || *pieces[0].SymbolName != "run" || pieces[0].ChunkType != "function" {
		t.Error("first piece must inherit the symbol")
	}
	for i, c := range pieces[1:] {
		if c.SymbolName != nil || c.ChunkType != "block" {
			t.Errorf("piece %d must be an anonymous block, got type %q", i+1, c.ChunkType)
		}
	}
}

// TestTokenSplitLineNumbers — a piece that starts mid-file must report the
// line it actually starts on, or `cix search` sends the reader to the wrong
// place.
func TestTokenSplitLineNumbers(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("word word word word word\n")
	}
	b := fakeBudget{maxInput: 4096}

	chunks := splitChunkTokens(Chunk{Content: sb.String(), FilePath: "x.txt", StartLine: 1}, b, 20)
	line := 1
	for i, c := range chunks {
		if c.StartLine != line {
			t.Errorf("chunk %d starts at line %d, expected %d", i, c.StartLine, line)
		}
		line += strings.Count(c.Content, "\n")
	}
}

func strPtr(s string) *string { return &s }
