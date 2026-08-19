package vectorstore

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Page layout — what a scan's cost is actually made of.
//
// Search latency on a developer's laptop is not a portable number: it is a
// statement about that machine's disk, page cache and core count. What IS
// portable is how many database pages a full-collection scan is obliged to
// touch, because SQLite's layout rules are the same everywhere. Multiply pages
// by the target machine's read throughput and you have its latency; assert on
// pages and the assertion means the same thing in CI, on a Mac with NVMe, and
// on the 2-vCPU production box whose page cache is smaller than its index.
// ---------------------------------------------------------------------------

// scanPages reports the pages a full scan must read, from whichever table the
// scan actually walks.
//
// dbstat walks the b-tree page by page, which is the only way to see overflow
// chains: they show up in no COUNT, in no per-table file size, and a row that
// spills is indistinguishable from one that does not until you look at its
// pages.
func scanPages(t *testing.T, s *Store, table string) (leaf, overflow, rows int64) {
	t.Helper()
	err := s.db.QueryRow(`SELECT COALESCE(SUM(pagetype='leaf'), 0),
	                             COALESCE(SUM(pagetype='overflow'), 0)
	                        FROM dbstat WHERE name = ?`, table).Scan(&leaf, &overflow)
	if err != nil {
		t.Fatalf("dbstat(%s): %v", table, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows); err != nil {
		t.Fatalf("count(%s): %v", table, err)
	}
	return leaf, overflow, rows
}

// scanTable is the table a search walks, and TestLayoutMeasuresTheScannedTable
// checks that scanQ8SQL still names it — otherwise a change of scan source
// would leave every measurement below pointed at a table nobody reads, and the
// numbers would keep passing while meaning nothing.
const scanTable = "vectors_q8"

// fillDim writes n rows of dimension dim into one collection.
func fillDim(t *testing.T, s *Store, project string, n, dim int) {
	t.Helper()
	r := rand.New(rand.NewSource(7))
	chunks := make([]Chunk, n)
	embs := make([][]float32, n)
	for i := range chunks {
		chunks[i] = Chunk{
			Content:    "package main\n\nfunc main() {}\n",
			FilePath:   fmt.Sprintf("pkg/mod%03d/file%03d.go", i%16, i),
			StartLine:  i*10 + 1,
			EndLine:    i*10 + 9,
			ChunkType:  "function",
			SymbolName: fmt.Sprintf("Handler%03d", i),
			Language:   "go",
		}
		embs[i] = randNorm(r, dim)
	}
	if err := s.UpsertChunks(context.Background(), project, chunks, embs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

// TestScanPackingEfficiency pins how much of what a scan reads is the data it
// came for.
//
// SQLite keeps a row inside its leaf page only while the payload fits
// usable-35 bytes (8157 on our 8 KiB pages). Past that it keeps about a
// kilobyte local and puts the rest in a chain of overflow pages. Neither the
// row size nor the crossing of that line is visible anywhere in the schema —
// and the row size is set by an operator choosing output_dimension in a config
// file. Measured on this schema, three dimensions behave in three different
// ways:
//
//	 768   3.1 kB row   two rows share a leaf page      4096 B/vector   1.33x
//	1024   4.1 kB row   one row per leaf page           8192 B/vector   2.00x
//	2048   8.2 kB row   leaf slice + one overflow page  9216 B/vector   1.12x
//
// The interesting line is 1024, not 2048. Overflow sounds like the pathology
// and is not: at 2048 the overflow page is nearly full, so the scan reads
// 9216 bytes to obtain 8192 useful ones. At 1024 nothing overflows and the
// scan still reads 8192 bytes to obtain 4096, because two 4.1 kB rows cannot
// share an 8 KiB page and the second half of every page is air. An operator
// who halves output_dimension to save disk and time gets half the vector
// quality for 89% of the I/O.
//
// The assertion is the ratio, so it survives a change of dimension, of page
// size, or of which columns live in this table.
func TestScanPackingEfficiency(t *testing.T) {
	// A scan that reads more than 1.4x the bytes it needs is spending more on
	// structure than any layout choice should cost. 1.33x — two rows to a
	// page with the page header and cell pointers on top — is what a healthy
	// packing looks like, and is what both 768-dim float32 and 2048-dim int8
	// achieve.
	const maxRatio = 1.4

	for _, dim := range []int{768, 1024, 2048} {
		t.Run(fmt.Sprintf("dim%d", dim), func(t *testing.T) {
			s := openStore(t)
			fillDim(t, s, "/layout", 400, dim)

			leaf, overflow, rows := scanPages(t, s, scanTable)
			perVec := float64((leaf+overflow)*pageSize) / float64(rows)
			// One byte per component: what the scan reads, against what it
			// reads it for.
			payload := float64(dim)
			ratio := perVec / payload

			t.Logf("dim=%d rows=%d leaf=%d overflow=%d  %.0f B/vector  %.2fx payload",
				dim, rows, leaf, overflow, perVec, ratio)

			if ratio > maxRatio {
				t.Errorf("scan reads %.0f B per %d-dim vector to obtain %.0f B of embedding "+
					"(%.2fx, limit %.2fx): leaf=%d overflow=%d over %d rows",
					perVec, dim, payload, ratio, maxRatio, leaf, overflow, rows)
			}
		})
	}
}

// TestScanBytesPerVectorBudget is the absolute number, and the one the search
// work exists to move.
//
// Packing efficiency says the scan wastes little; it says nothing about the
// scan being affordable. At 2048 dimensions a well-packed float32 scan still
// reads 9 kB per vector, and a workspace query scans every collection: on the
// 45-repo fixture that is 1.9M vectors, about 17 GB of reads for one search.
// No page cache on an 8 GB box holds that, so production pays it at disk
// speed, every query, per repo.
//
// The budget below is what the scan costs when it reads a compact
// representation instead of the float32 original — the float32 blob stays on
// disk for anything that needs exact scores, but the scan stops reading it.
func TestScanBytesPerVectorBudget(t *testing.T) {
	const dim = 2048
	// 2048 int8 components + row overhead, three rows to a page.
	const maxBytesPerVector = 3072

	s := openStore(t)
	fillDim(t, s, "/budget", 400, dim)

	leaf, overflow, rows := scanPages(t, s, scanTable)
	perVec := float64((leaf+overflow)*pageSize) / float64(rows)
	t.Logf("dim=%d rows=%d leaf=%d overflow=%d  %.0f B/vector", dim, rows, leaf, overflow, perVec)

	if perVec > maxBytesPerVector {
		t.Errorf("scan reads %.0f B per vector at %d dims, budget %d B: "+
			"a 1.9M-vector workspace query moves %.1f GB instead of %.1f GB",
			perVec, dim, maxBytesPerVector,
			perVec*1.9e6/1e9, float64(maxBytesPerVector)*1.9e6/1e9)
	}
}

// TestLayoutMeasuresTheScannedTable keeps the two constants honest. dbstat is
// asked about a table by name, and a name is exactly the kind of thing that
// survives a refactor that moved the data somewhere else.
func TestLayoutMeasuresTheScannedTable(t *testing.T) {
	if !strings.Contains(scanQ8SQL, " "+scanTable+" ") {
		t.Fatalf("the scan reads a table other than %q:\n%s", scanTable, scanQ8SQL)
	}
}
