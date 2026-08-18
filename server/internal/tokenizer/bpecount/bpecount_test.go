package bpecount

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// tokenizerPath is the real voyage-code-3 tokenizer.json. The tests that need
// it skip when it is absent so a checkout without the 7 MB file still builds
// and tests clean.
const tokenizerPath = "../../../../loadtests/bench/voyage-code-3.tokenizer.json"

func load(t *testing.T) *Counter {
	t.Helper()
	if _, err := os.Stat(tokenizerPath); err != nil {
		t.Skip("tokenizer.json not present")
	}
	c, err := Load(tokenizerPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

// TestCountMatchesReference pins the counts that were verified against
// Voyage's own usage.total_tokens and the HuggingFace Rust tokenizer. The tab
// cases are the ones a RE2 rewrite of the pre-tokenizer regex gets wrong: the
// `\s+(?!\S)` lookahead cannot be expressed, and a naive rewrite absorbs a
// leading tab into the punctuation branch that may only absorb a space.
func TestCountMatchesReference(t *testing.T) {
	c := load(t)
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"func main() {\n\tfmt.Println(\"hi\")\n}\n", 10},
		{"\t\t\"a\"", 4},
		{"\t\t\t\"end\": {", 6},
		{"a\t\t-b", 4},
		{"class A:\n    def g(self):\n                x = 1\n", 14},
		{"hello world", 2},
		{"#ifdef USE_THREADS", 3},
		{"", 0},
	} {
		if got := c.Count(tc.in); got != tc.want {
			t.Errorf("Count(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestNFCNormalisation covers the second gap in the ollama tokenizer: the
// tokenizer.json declares an NFC normalizer, and skipping it makes decomposed
// input cost an extra token.
func TestNFCNormalisation(t *testing.T) {
	c := load(t)
	nfc := "caf\u00e9"  // é as one code point
	nfd := "cafe\u0301" // e + combining acute
	if a, b := c.Count(nfc), c.Count(nfd); a != b {
		t.Errorf("NFC %d != NFD %d — normaliser not applied", a, b)
	}
}

// TestSplitPointsAreExact is the property the splitter exists for: because BPE
// merges never cross a pre-token boundary, the pieces must add up to the whole
// and none may exceed the budget.
func TestSplitPointsAreExact(t *testing.T) {
	c := load(t)
	src := ""
	for i := 0; i < 400; i++ {
		src += "func handler(w http.ResponseWriter, r *http.Request) {\n\tdefer r.Body.Close()\n}\n"
	}
	const budget = 500

	offsets, total := c.SplitPoints(src, budget)
	if total != c.Count(src) {
		t.Fatalf("SplitPoints total %d != Count %d", total, c.Count(src))
	}
	if len(offsets) == 0 {
		t.Fatalf("expected cuts for %d tokens at budget %d", total, budget)
	}

	sum, prev := 0, 0
	for _, off := range append(offsets, len(src)) {
		n := c.Count(src[prev:off])
		if n > budget {
			t.Errorf("piece [%d:%d] is %d tokens, over budget %d", prev, off, n, budget)
		}
		sum += n
		prev = off
	}
	if sum != total {
		t.Errorf("pieces sum to %d, whole is %d — merges leaked across a cut", sum, total)
	}
}

// TestSplitPointsFitsAlready — a text under budget must not be cut.
func TestSplitPointsFitsAlready(t *testing.T) {
	c := load(t)
	offsets, total := c.SplitPoints("package main\n", 1000)
	if offsets != nil {
		t.Errorf("expected no cuts, got %v", offsets)
	}
	if total == 0 {
		t.Error("total should be counted even when no cut is needed")
	}
}

// TestSplitInsidePreToken covers the one case boundaries cannot serve: a
// single pre-token bigger than the budget (base64 blobs, minified lines).
func TestSplitPointsOversizePreToken(t *testing.T) {
	c := load(t)
	// One unbroken run of a single character class. "aB3" repeated would NOT
	// do: the pre-tokenizer breaks letters from digits, so it yields 2-byte
	// pre-tokens that never exceed the budget and the splitInside path this
	// test exists for is never entered.
	blob := strings.Repeat("a", 4000)
	const budget = 100
	offsets, _ := c.SplitPoints(blob, budget)
	if len(offsets) == 0 {
		t.Fatal("expected the blob to be cut")
	}
	prev := 0
	for _, off := range append(offsets, len(blob)) {
		if n := c.Count(blob[prev:off]); n > budget {
			t.Errorf("piece [%d:%d] is %d tokens, over budget %d", prev, off, n, budget)
		}
		prev = off
	}
}

// --- CI coverage without the 7 MB file ---
//
// The golden-count tests above need the real voyage-code-3 tokenizer.json,
// which is not in the repo, so they skip on a clean checkout. The mechanics —
// pre-token splitting, merge application, the additivity SplitPoints relies on
// — do not need that vocabulary. A hand-built merge table exercises them, so
// CI still fails if the splitter or the merge loop regresses.
func syntheticCounter(t *testing.T) *Counter {
	t.Helper()
	// Merges are ranked: "a b" collapses first, then "ab c".
	c, err := LoadBytes(syntheticJSON("a b", "ab c", "f u", "fu n"))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	return c
}

// TestSyntheticMergesApply — with only "a b" known, "abc" costs one merge plus
// the leftover byte; the second merge then folds that leftover in.
func TestSyntheticMergesApply(t *testing.T) {
	c := syntheticCounter(t)
	if got, want := c.Count("abc"), 1; got != want {
		t.Errorf(`Count("abc") = %d, want %d (a+b -> ab, ab+c -> abc)`, got, want)
	}
	if got, want := c.Count("ab"), 1; got != want {
		t.Errorf(`Count("ab") = %d, want %d`, got, want)
	}
	if got, want := c.Count("acb"), 3; got != want {
		t.Errorf(`Count("acb") = %d, want %d (no merge applies)`, got, want)
	}
}

// TestSyntheticAdditivity is the property the whole splitter rests on: BPE
// never merges across a pre-token boundary, so counts add up. If a future
// change made merges span boundaries, cuts would silently produce over-budget
// pieces — this catches it without needing the real vocabulary.
func TestSyntheticAdditivity(t *testing.T) {
	c := syntheticCounter(t)
	const text = "abc abc\n\tabc fun fun"
	whole := c.Count(text)

	offsets, total := c.SplitPoints(text, 3)
	if total != whole {
		t.Fatalf("SplitPoints total %d != Count %d", total, whole)
	}
	sum, prev := 0, 0
	for _, off := range append(offsets, len(text)) {
		n := c.Count(text[prev:off])
		if n > 3 {
			t.Errorf("piece %q is %d tokens, over budget 3", text[prev:off], n)
		}
		sum += n
		prev = off
	}
	if sum != whole {
		t.Errorf("pieces sum to %d, whole is %d", sum, whole)
	}
}

// TestPreTokenBoundaries pins the hand-rolled splitter against the branches of
// the Qwen2 pattern that a RE2 rewrite gets wrong — whitespace runs, and a tab
// that must NOT be absorbed into the punctuation branch.
func TestPreTokenBoundaries(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"a b", []string{"a", " b"}},
		{"a  b", []string{"a", " ", " b"}},
		// A tab is a legal single-character prefix for the letter branch
		// ([^\r\n\p{L}\p{N}]?\p{L}+), so it attaches to what follows.
		{"x\n\ty", []string{"x", "\n", "\ty"}},
		// The lookahead branch \s+(?!\S) matches a whitespace run only when
		// nothing non-space follows it. The first tab qualifies (a tab
		// follows); the second does not (a quote follows) and falls through
		// to plain \s+. Hence two separate pre-tokens, not one run — this is
		// precisely what a RE2 rewrite of the pattern gets wrong.
		{"\t\t\"a\"", []string{"\t", "\t", "\"a", "\""}},
		{"it's", []string{"it", "'s"}},
		{"a1", []string{"a", "1"}},
	} {
		var got []string
		s := tc.in
		for len(s) > 0 {
			n := nextToken(s)
			if n <= 0 {
				t.Fatalf("nextToken(%q) returned %d", s, n)
			}
			got = append(got, s[:n])
			s = s[n:]
		}
		if len(got) != len(tc.want) {
			t.Errorf("split(%q) = %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("split(%q) = %q, want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

// TestNFDPiecesRespectBudget covers input that is not already NFC, where the
// normalised copy counting works on has different byte offsets from the
// caller's string. Before the fix, offsets computed against the normalised
// copy were returned as-is: decomposed text made them land mid-rune, and the
// composition exclusions at U+0958..U+095F (which DEcompose under NFC, making
// the normalised form longer) pushed them past the end of the original, so the
// caller's slice expression panicked. Every other fixture in this file is
// ASCII or already-NFC and could not see it.
func TestNFDPiecesRespectBudget(t *testing.T) {
	c := load(t)
	for _, raw := range []string{
		strings.Repeat("// café comment here\n", 200),
		strings.Repeat("x क़ख़ग़ ", 400),
		strings.Repeat("Ώ ", 900),
	} {
		offs, total := c.SplitPoints(raw, 50)
		prev := 0
		for _, off := range append(offs, len(raw)) {
			if off > len(raw) || off < prev {
				t.Fatalf("bad offset %d (len %d, prev %d)", off, len(raw), prev)
			}
			if n := c.Count(raw[prev:off]); n > 50 {
				t.Errorf("piece [%d:%d] is %d tokens, over budget 50", prev, off, n)
			}
			prev = off
		}
		if total != c.Count(raw) {
			t.Errorf("total %d != Count %d", total, c.Count(raw))
		}
	}
}

// TestSplitInsideChargesTail — an over-budget pre-token used to leave its tail
// uncounted, letting the next piece stack a full budget on top of it.
func TestSplitInsideChargesTail(t *testing.T) {
	c, err := LoadBytes(syntheticJSON("a b"))
	if err != nil {
		t.Fatal(err)
	}
	in := strings.Repeat("x", 10) + " " + strings.Repeat("y", 4)
	offs, _ := c.SplitPoints(in, 5)
	prev := 0
	for _, off := range append(offs, len(in)) {
		if n := c.Count(in[prev:off]); n > 5 {
			t.Errorf("piece %q is %d tokens, over budget 5", in[prev:off], n)
		}
		prev = off
	}
}

// syntheticJSON builds a tokenizer.json with a hand-picked merge table and the
// real pipeline sections, so LoadBytes's compatibility check sees what it
// expects. Tests that only exercise merging still have to declare the pipeline
// they are pretending to be — which is the point of the check.
func syntheticJSON(merges ...string) []byte {
	doc := map[string]any{
		"model":      map[string]any{"type": "BPE", "merges": merges},
		"normalizer": map[string]any{"type": "NFC"},
		"pre_tokenizer": map[string]any{
			"type": "Sequence",
			"pretokenizers": []any{
				map[string]any{"type": "Split", "pattern": map[string]any{"Regex": qwen2SplitPattern}},
				map[string]any{"type": "ByteLevel"},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return b
}

// TestRejectsForeignPipeline — a GPT-2 or o200k tokenizer.json parses cleanly
// and declares BPE, but its pre-tokenizer is not the one implemented here. It
// must be refused rather than counted wrongly.
func TestRejectsForeignPipeline(t *testing.T) {
	for name, doc := range map[string]string{
		"gpt2 (no Split stage)": `{"model":{"type":"BPE","merges":["a b"]},
			"normalizer":null,"pre_tokenizer":{"type":"ByteLevel"}}`,
		"o200k (different pattern)": `{"model":{"type":"BPE","merges":["a b"]},
			"normalizer":{"type":"NFC"},"pre_tokenizer":{"type":"Sequence","pretokenizers":[
			{"type":"Split","pattern":{"Regex":"[^\\r\\n\\p{L}\\p{N}]?[\\p{L}]+"}},
			{"type":"ByteLevel"}]}}`,
	} {
		if _, err := LoadBytes([]byte(doc)); err == nil {
			t.Errorf("%s: expected a load error, got none", name)
		}
	}
}

// TestMultibyteRunsTerminate covers runs of multi-byte runes long enough to
// exceed the budget as a single pre-token — a box-drawing comment separator,
// an arrow run, a run of combining marks.
//
// The byte-offset binary search this replaced aligned its midpoint to a rune
// start by DECREMENTING, so when alignment pulled the midpoint below lo, the
// next lo = mid+1 did not advance and the search spun forever. It took no
// error path and produced no output: the indexing worker simply stopped. All
// three inputs below hung at budgets 5 and 50.
func TestMultibyteRunsTerminate(t *testing.T) {
	c := load(t)
	inputs := map[string]string{
		"box drawing separator": "// " + strings.Repeat("\u2500", 400) + "\n",
		"arrow run":             strings.Repeat("\u2192", 400),
		"combining marks":       strings.Repeat("\u0301", 50),
		"composition exclusion": strings.Repeat("\u0958", 2000),
	}
	for _, budget := range []int{5, 50} {
		for name, in := range inputs {
			done := make(chan struct{})
			var offs []int
			go func(s string, b int) {
				offs, _ = c.SplitPoints(s, b)
				close(done)
			}(in, budget)

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("%s at budget %d: SplitPoints did not return", name, budget)
			}

			prev := 0
			for _, off := range append(offs, len(in)) {
				if off > len(in) || off < prev {
					t.Fatalf("%s: offset %d out of range (len %d, prev %d)", name, off, len(in), prev)
				}
				if !utf8.ValidString(in[prev:off]) {
					t.Errorf("%s: piece [%d:%d] is not valid UTF-8 — cut mid-rune", name, prev, off)
				}
				prev = off
			}
		}
	}
}
