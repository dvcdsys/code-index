// Package bpecount is a minimal, count-only byte-level BPE tokenizer for
// GPT-2/Qwen2-style tokenizer.json files (voyage-code-3, Qwen2, GPT-4o…).
//
// It reproduces the HuggingFace pipeline:
//
//	normalizer  = NFC
//	pretokenize = Split(Qwen2 regex, Isolated) + ByteLevel(add_prefix_space=false)
//	model       = BPE (greedy lowest-rank merge)
//
// The Split regex contains `\s+(?!\S)`, a negative lookahead Go's RE2
// cannot express, so the splitter is hand-rolled rather than compiled.
// Only a COUNT is produced — no ids, no offsets.
package bpecount

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Counter holds the merge table and a memo of pre-token → token count.
type Counter struct {
	merges map[string]int32 // "left right" -> rank

	mu   sync.RWMutex
	memo map[string]int
}

type tokJSON struct {
	Model struct {
		Type   string          `json:"type"`
		Merges json.RawMessage `json:"merges"`
	} `json:"model"`
}

// Load reads a tokenizer.json and keeps only what a count needs: the merges.
func Load(path string) (*Counter, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}

func LoadBytes(b []byte) (*Counter, error) {
	var tj tokJSON
	if err := json.Unmarshal(b, &tj); err != nil {
		return nil, err
	}
	if tj.Model.Type != "BPE" {
		return nil, fmt.Errorf("bpecount: unsupported model type %q", tj.Model.Type)
	}
	// merges is either ["a b", ...] (v1) or [["a","b"], ...] (v2).
	var flat []string
	m := make(map[string]int32)
	if err := json.Unmarshal(tj.Model.Merges, &flat); err == nil {
		for i, s := range flat {
			m[s] = int32(i)
		}
	} else {
		var pairs [][]string
		if err := json.Unmarshal(tj.Model.Merges, &pairs); err != nil {
			return nil, fmt.Errorf("bpecount: merges: %w", err)
		}
		for i, p := range pairs {
			if len(p) == 2 {
				m[p[0]+" "+p[1]] = int32(i)
			}
		}
	}
	return &Counter{merges: m, memo: make(map[string]int, 1<<16)}, nil
}

// ---------- byte-level alphabet (GPT-2 bytes_to_unicode) ----------

var byteRune [256]rune

func init() {
	for b := 0; b < 256; b++ {
		r := rune(b)
		switch {
		case r == 0xad:
			r = 0x143
		case r <= 0x20:
			r += 0x100
		case r >= 0x7f && r <= 0xa0:
			r += 0xa2
		}
		byteRune[b] = r
	}
}

// ---------- hand-rolled splitter ----------
//
// Qwen2 pattern, alternation tried left to right (Perl leftmost-first):
//
//	(?i:'s|'t|'re|'ve|'m|'ll|'d)
//	[^\r\n\p{L}\p{N}]?\p{L}+
//	\p{N}
//	 ?[^\s\p{L}\p{N}]+[\r\n]*
//	\s*[\r\n]+
//	\s+(?!\S)
//	\s+
//
// Every rune is covered by some branch, so Split(Isolated) yields no gaps.

func isL(r rune) bool  { return unicode.IsLetter(r) }
func isN(r rune) bool  { return unicode.IsNumber(r) }
func isWS(r rune) bool { return unicode.IsSpace(r) }
func isNL(r rune) bool { return r == '\r' || r == '\n' }

var contractions = []string{"s", "t", "re", "ve", "m", "ll", "d"}

// nextToken returns the byte length of the pre-token starting at s[0].
func nextToken(s string) int {
	r0, w0 := decode(s, 0)

	// A: contraction
	if r0 == '\'' && len(s) > w0 {
		low := strings.ToLower(s)
		for _, c := range contractions {
			if strings.HasPrefix(low[w0:], c) {
				return w0 + len(c)
			}
		}
	}

	// B: [^\r\n\p{L}\p{N}]? \p{L}+
	{
		i := 0
		if !isNL(r0) && !isL(r0) && !isN(r0) {
			i = w0
		}
		j := i
		for j < len(s) {
			r, w := decode(s, j)
			if !isL(r) {
				break
			}
			j += w
		}
		if j > i { // at least one letter followed
			return j
		}
	}

	// C: single \p{N}
	if isN(r0) {
		return w0
	}

	// D: " ?" [^\s\p{L}\p{N}]+ [\r\n]*
	{
		i := 0
		if r0 == ' ' {
			i = w0
		}
		j := i
		for j < len(s) {
			r, w := decode(s, j)
			if isWS(r) || isL(r) || isN(r) {
				break
			}
			j += w
		}
		if j > i {
			for j < len(s) {
				r, w := decode(s, j)
				if !isNL(r) {
					break
				}
				j += w
			}
			return j
		}
	}

	// E/F/G: whitespace run.
	if isWS(r0) {
		// maximal whitespace run
		end := 0
		lastNL := -1
		for end < len(s) {
			r, w := decode(s, end)
			if !isWS(r) {
				break
			}
			if isNL(r) {
				lastNL = end + w
			}
			end += w
		}
		// E: \s*[\r\n]+ — run truncated after its LAST \r or \n.
		if lastNL >= 0 {
			return lastNL
		}
		// F: \s+(?!\S) — whole run at EOF, else run minus its last rune.
		if end == len(s) {
			return end
		}
		_, lw := decodeLast(s[:end])
		if end-lw > 0 {
			return end - lw
		}
		// G: \s+ (single whitespace rune followed by a non-space)
		return end
	}

	// Unreachable for well-formed input; make progress anyway.
	return w0
}

func decode(s string, i int) (rune, int) {
	if s[i] < utf8.RuneSelf {
		return rune(s[i]), 1
	}
	return utf8.DecodeRuneInString(s[i:])
}

func decodeLast(s string) (rune, int) {
	return utf8.DecodeLastRuneInString(s)
}

// ---------- BPE ----------

func (c *Counter) bpeLen(piece string) int {
	c.mu.RLock()
	n, ok := c.memo[piece]
	c.mu.RUnlock()
	if ok {
		return n
	}
	n = c.bpe(piece)
	c.mu.Lock()
	if len(c.memo) < 1<<20 {
		c.memo[piece] = n
	}
	c.mu.Unlock()
	return n
}

// node is one symbol in the doubly-linked list the merge loop walks.
type node struct {
	prev, next int
	s          string
	alive      bool
}

// cand is a candidate merge sitting in the priority queue.
type cand struct {
	rank int32
	l, r int
	// len of the two symbols when the candidate was pushed; a stale entry
	// (one side already merged into something longer) is detected by comparing.
	ll, rl int
}

type candHeap []cand

func (h candHeap) Len() int { return len(h) }
func (h candHeap) Less(i, j int) bool {
	if h[i].rank != h[j].rank {
		return h[i].rank < h[j].rank
	}
	return h[i].l < h[j].l // ties: leftmost first, matching HF
}
func (h candHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *candHeap) Push(x any)   { *h = append(*h, x.(cand)) }
func (h *candHeap) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	*h = old[:n-1]
	return v
}

// bpe applies the greedy lowest-rank-first merge with a linked list + heap,
// so a pathological single pre-token (a 30 KB run of '-' or one enormous
// identifier) stays near-linear instead of the O(n^2) rescan a naive loop does.
func (c *Counter) bpe(piece string) int {
	nodes := make([]node, 0, len(piece))
	for _, r := range piece {
		i := len(nodes)
		nodes = append(nodes, node{prev: i - 1, next: i + 1, s: string(r), alive: true})
	}
	n := len(nodes)
	if n < 2 {
		return n
	}
	nodes[n-1].next = -1

	h := make(candHeap, 0, n)
	push := func(l, r int) {
		if l < 0 || r < 0 || r >= n {
			return
		}
		if rk, ok := c.merges[nodes[l].s+" "+nodes[r].s]; ok {
			h = append(h, cand{rank: rk, l: l, r: r, ll: len(nodes[l].s), rl: len(nodes[r].s)})
		}
	}
	for i := 0; i+1 < n; i++ {
		push(i, i+1)
	}
	heap.Init(&h)

	live := n
	for h.Len() > 0 {
		cd := heap.Pop(&h).(cand)
		l, r := cd.l, cd.r
		// Reject stale entries: either side merged away or grew since push.
		if !nodes[l].alive || !nodes[r].alive || nodes[l].next != r ||
			len(nodes[l].s) != cd.ll || len(nodes[r].s) != cd.rl {
			continue
		}
		nodes[l].s += nodes[r].s
		nodes[r].alive = false
		nodes[l].next = nodes[r].next
		if nodes[r].next >= 0 {
			nodes[nodes[r].next].prev = l
		}
		live--
		if live == 1 {
			return 1
		}
		if p := nodes[l].prev; p >= 0 {
			if rk, ok := c.merges[nodes[p].s+" "+nodes[l].s]; ok {
				heap.Push(&h, cand{rank: rk, l: p, r: l, ll: len(nodes[p].s), rl: len(nodes[l].s)})
			}
		}
		if nx := nodes[l].next; nx >= 0 {
			if rk, ok := c.merges[nodes[l].s+" "+nodes[nx].s]; ok {
				heap.Push(&h, cand{rank: rk, l: l, r: nx, ll: len(nodes[l].s), rl: len(nodes[nx].s)})
			}
		}
	}
	return live
}

// Count returns the number of tokens voyage/HF would produce for text.
func (c *Counter) Count(text string) int {
	if text == "" {
		return 0
	}
	s := norm.NFC.String(text)
	total := 0
	var sb strings.Builder
	for len(s) > 0 {
		n := nextToken(s)
		if n <= 0 {
			n = 1
		}
		piece := s[:n]
		s = s[n:]
		// ByteLevel map
		sb.Reset()
		sb.Grow(len(piece) * 2)
		for i := 0; i < len(piece); i++ {
			sb.WriteRune(byteRune[piece[i]])
		}
		total += c.bpeLen(sb.String())
	}
	return total
}

// SplitPoints returns the byte offsets at which text must be cut so that no
// piece exceeds budget tokens, plus the total token count of the whole text.
//
// The cuts are exact, not estimated, and they need no search. BPE merges never
// cross a pre-token boundary in this pipeline (Split runs with Isolated
// behaviour, and ByteLevel+BPE are applied per pre-token), so a text's token
// count is the SUM of its pre-tokens' counts. Cutting on a pre-token boundary
// therefore leaves both sides tokenising exactly as they did inside the whole:
// the parts always add up to the total, with no drift to correct for.
//
// One pass, left to right, reusing the same memo Count uses — so a split costs
// what a count costs, plus the offsets slice.
//
// A single pre-token larger than budget cannot be honoured on a boundary (its
// merges DO interact internally). Rather than silently emit an over-budget
// piece, splitInside falls back to a binary search on bytes within that one
// pre-token. That path is for base64 blobs and minified lines with no
// whitespace; on a 45-repo corpus it fires for 5 chunks in 1.9M.
//
// Offsets are cut points only: nil means the text already fits.
func (c *Counter) SplitPoints(text string, budget int) (offsets []int, total int) {
	if text == "" {
		return nil, 0
	}
	if budget <= 0 {
		return nil, c.Count(text)
	}
	s := norm.NFC.String(text)

	acc := 0    // tokens accumulated in the current piece
	pos := 0    // byte offset into s
	var sb strings.Builder
	for pos < len(s) {
		n := nextToken(s[pos:])
		if n <= 0 {
			n = 1
		}
		piece := s[pos : pos+n]

		sb.Reset()
		sb.Grow(len(piece) * 2)
		for i := 0; i < len(piece); i++ {
			sb.WriteRune(byteRune[piece[i]])
		}
		tk := c.bpeLen(sb.String())

		switch {
		case tk > budget:
			// Does not fit even alone. Close the current piece, then cut
			// inside this pre-token.
			if acc > 0 {
				offsets = append(offsets, pos)
				acc = 0
			}
			inner := c.splitInside(s[pos:pos+n], budget)
			for _, off := range inner {
				offsets = append(offsets, pos+off)
			}
			// Tail of the pre-token starts a fresh piece; its token count is
			// unknown without re-counting, so charge it conservatively as a
			// full budget minus nothing and let the next boundary close it.
			acc = 0
		case acc+tk > budget:
			offsets = append(offsets, pos)
			acc = tk
		default:
			acc += tk
		}
		total += tk
		pos += n
	}
	return offsets, total
}

// splitInside cuts one over-budget pre-token by binary search on its bytes.
// Inside a pre-token counts are not additive, so every candidate cut is
// re-counted — but the search converges in a handful of probes because
// bytes-per-token is near-constant within a homogeneous run.
func (c *Counter) splitInside(piece string, budget int) []int {
	var cuts []int
	start := 0
	for start < len(piece) {
		if c.Count(piece[start:]) <= budget {
			break
		}
		lo, hi := start+1, len(piece)
		best := start + 1
		for lo <= hi {
			mid := (lo + hi) / 2
			for mid > start && mid < len(piece) && !utf8.RuneStart(piece[mid]) {
				mid--
			}
			if mid <= start {
				break
			}
			if c.Count(piece[start:mid]) <= budget {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best >= len(piece) {
			break
		}
		cuts = append(cuts, best)
		start = best
	}
	return cuts
}
