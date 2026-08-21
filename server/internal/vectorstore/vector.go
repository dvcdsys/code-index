package vectorstore

import (
	"encoding/binary"
	"math"
	"unsafe"
)

// ---------------------------------------------------------------------------
// blob <-> []float32
//
// Embeddings are stored as raw little-endian float32 BLOBs. The dimension is
// implied by the blob length (len/4) rather than declared in the schema, so a
// namespace whose model changes dimension is detected per row instead of
// failing a column constraint.
// ---------------------------------------------------------------------------

// littleEndian is resolved once at init; the zero-copy decode below is only
// valid on a little-endian machine.
var littleEndian = func() bool {
	var x uint16 = 1
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

// blobFloats returns a []float32 view of b.
//
// The fast path reinterprets the bytes in place (no copy, no allocation) and
// is only taken on a little-endian machine when the buffer is 4-byte aligned;
// the returned slice aliases b and is therefore only valid until the caller
// advances the sql.Rows cursor. Everything else decodes into scratch, which is
// grown as needed and returned so the caller can reuse it across rows.
func blobFloats(b []byte, scratch []float32) (vec, newScratch []float32) {
	n := len(b) / 4
	if n == 0 {
		return nil, scratch
	}
	if littleEndian && len(b)%4 == 0 && uintptr(unsafe.Pointer(&b[0]))%4 == 0 {
		return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), n), scratch
	}
	if cap(scratch) < n {
		scratch = make([]float32, n)
	}
	scratch = scratch[:n]
	for i := 0; i < n; i++ {
		scratch[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return scratch, scratch
}

// floatsBlob encodes a vector as a little-endian float32 blob.
func floatsBlob(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// ---------------------------------------------------------------------------
// similarity
// ---------------------------------------------------------------------------

// isNormalizedTolerance mirrors chromem-go's isNormalizedPrecisionTolerance
// (vector.go:8). Keeping the same tolerance keeps the "renormalise on write"
// decision identical, so a vector that chromem stored verbatim is also stored
// verbatim here.
const isNormalizedTolerance = 1e-6

func isNormalized(v []float32) bool {
	var sqSum float64
	for _, x := range v {
		sqSum += float64(x) * float64(x)
	}
	return math.Abs(math.Sqrt(sqSum)-1) < isNormalizedTolerance
}

// normalizeVector returns the L2-normalised copy of v. Mirrors chromem-go's
// normalizeVector, including its behaviour on the zero vector (division by
// zero yields NaN there; we return the input unchanged instead, which is the
// only difference and strictly safer).
func normalizeVector(v []float32) []float32 {
	var sqSum float64
	for _, x := range v {
		sqSum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sqSum))
	if norm == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}

// dot is the cosine similarity for L2-normalised vectors. Four-way unrolled —
// the standard Go idiom; no assembly, no SIMD.
//
// Returns 0 for a length mismatch. Callers must check dimensions themselves
// when a mismatch is meaningful (the scan skips such rows rather than scoring
// them 0, so a stray wrong-dimension row can never win a slot).
func dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		x := a[i : i+4 : i+4]
		y := b[i : i+4 : i+4]
		s0 += x[0] * y[0]
		s1 += x[1] * y[1]
		s2 += x[2] * y[2]
		s3 += x[3] * y[3]
	}
	sum := s0 + s1 + s2 + s3
	for ; i < len(a); i++ {
		sum += a[i] * b[i]
	}
	return sum
}

// ---------------------------------------------------------------------------
// top-K
// ---------------------------------------------------------------------------

// candidate is one scored row. Only the doc_id is kept during the scan — the
// metadata and the chunk text of the K winners are fetched afterwards, so the
// scan never materialises a string it is about to throw away.
//
// doc_id rather than rowid because the identity has to survive a VACUUM.
// `vectors` has a composite PRIMARY KEY, so its rowid is implicit, and SQLite
// only promises to preserve implicit rowids across a VACUUM for tables with an
// INTEGER PRIMARY KEY — everywhere else it may renumber them. That is
// survivable while the rowid never leaves a single query, and fatal once a
// second table (vectors_q8) keys off it: the pairing would silently shift and
// every search would score one document with another one's vector.
type candidate struct {
	docID string
	score float32
}

// topK keeps the K highest scores in a min-heap of size K: the smallest kept
// score sits at h[0], so a losing candidate is rejected with one comparison.
//
// K is a parameter throughout; nothing in this package bakes in a default of
// 10. The heap is irrelevant next to the per-row streaming cost — measured
// +1.6% going from k=10 to k=500 — so a large limit costs essentially nothing.
type topK struct {
	h []candidate
	k int
}

func newTopK(k int) *topK { return &topK{h: make([]candidate, 0, min(k, 4096)), k: k} }

// qualifies reports whether score would enter the heap.
func (t *topK) qualifies(score float32) bool {
	return len(t.h) < t.k || score > t.h[0].score
}

func (t *topK) add(c candidate) {
	if len(t.h) < t.k {
		t.h = append(t.h, c)
		t.up(len(t.h) - 1)
		return
	}
	if c.score <= t.h[0].score {
		return
	}
	t.h[0] = c
	t.down(0)
}

func (t *topK) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if t.h[p].score <= t.h[i].score {
			return
		}
		t.h[p], t.h[i] = t.h[i], t.h[p]
		i = p
	}
}

func (t *topK) down(i int) {
	n := len(t.h)
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && t.h[l].score < t.h[m].score {
			m = l
		}
		if r < n && t.h[r].score < t.h[m].score {
			m = r
		}
		if m == i {
			return
		}
		t.h[i], t.h[m] = t.h[m], t.h[i]
		i = m
	}
}

// sorted returns the candidates in descending score order. Ties break on the
// lower doc_id so the ordering is deterministic across runs (SQLite may hand
// equal-scoring rows back in a different physical order after churn).
func (t *topK) sorted() []candidate {
	out := append([]candidate(nil), t.h...)
	// Insertion sort: k is small in every real call (10..500) and this avoids
	// dragging in sort.Slice's reflection-free-but-still-indirect machinery.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func less(a, b candidate) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.docID < b.docID
}

// ---------------------------------------------------------------------------
// int8 quantisation
//
// The scan's cost is the bytes it streams, and at 2048 dimensions a float32
// embedding is 8 KiB — more than an 8 KiB page can hold beside its own header,
// so every row also drags an overflow page behind it. Measured on the 45-repo
// fixture, one workspace query moves 17.6 GB.
//
// int8 makes that 2 KiB, which packs three rows to a page and reads 2.7 kB per
// vector: 3.4x less I/O and, measured, 3.0x less CPU in the dot product.
//
// The quantisation is per vector, not global. A single outlier component
// anywhere in a corpus would otherwise set the scale for every vector in it
// and crush the resolution of all the ordinary ones. Per-vector costs 8 bytes
// of REAL and makes each vector's own dynamic range the thing being spent.
//
// What this loses, measured against exact float32 ranking over 50 real
// query-side embeddings on the fixture's largest collection (60k vectors of
// ziglang/zig, voyage-code-3 @2048): recall@10 = 0.994 from the int8 ranking
// alone. Rescoring the shortlist on the float32 originals brought it to 1.000
// at k=10 and k=20 — the approximation misorders near-ties, it does not lose
// the documents, so re-reading a few dozen exact vectors recovered every one.
// That is why the float32 blob stays on disk: it is no longer read by the
// scan, only by the rescore.
//
// Note what that does and does not promise. The SCORES are exact — they come
// from the float32 vectors either way. The SET is an approximation measured at
// zero error here, not proved at zero in general; see q8Shortlist for the
// boundary case it cannot rule out.
// ---------------------------------------------------------------------------

// int8Max is the quantisation range. -128 is deliberately excluded: keeping
// the range symmetric means scale*q reconstructs -maxAbs and +maxAbs alike, so
// no component's sign carries a different error than its opposite.
const int8Max = 127

// quantizeInt8 encodes v as one signed byte per component plus the scale that
// undoes it: v[i] ~= scale * int8(blob[i]).
//
// A zero vector (or one of zero length) yields scale 0, which scores 0 against
// every query — the same answer the float32 dot product gives it.
func quantizeInt8(v []float32) (blob []byte, scale float32) {
	if len(v) == 0 {
		return nil, 0
	}
	var maxAbs float32
	for _, x := range v {
		if x < 0 {
			x = -x
		}
		if x > maxAbs {
			maxAbs = x
		}
	}
	if maxAbs == 0 {
		return make([]byte, len(v)), 0
	}
	scale = maxAbs / int8Max
	blob = make([]byte, len(v))
	for i, x := range v {
		r := float64(x) / float64(scale)
		q := math.Round(r)
		if q > int8Max {
			q = int8Max
		} else if q < -int8Max {
			q = -int8Max
		}
		blob[i] = byte(int8(q))
	}
	return blob, scale
}

// dotInt8 is the integer dot product of two quantised vectors.
//
// int32 cannot overflow here: every term is at most 127*127 = 16129, so it
// would take more than 133k dimensions to reach 2^31. Accumulating in int32
// rather than float32 also means the sum is exact — all the error in this path
// is in the quantisation, none of it in the arithmetic.
//
// Returns 0 for a length mismatch, matching dot().
func dotInt8(a, b []byte) int32 {
	if len(a) != len(b) {
		return 0
	}
	var s0, s1, s2, s3 int32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		x := a[i : i+4 : i+4]
		y := b[i : i+4 : i+4]
		s0 += int32(int8(x[0])) * int32(int8(y[0]))
		s1 += int32(int8(x[1])) * int32(int8(y[1]))
		s2 += int32(int8(x[2])) * int32(int8(y[2]))
		s3 += int32(int8(x[3])) * int32(int8(y[3]))
	}
	sum := s0 + s1 + s2 + s3
	for ; i < len(a); i++ {
		sum += int32(int8(a[i])) * int32(int8(b[i]))
	}
	return sum
}
