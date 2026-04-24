//go:build bench_chromem

// Bench 1 — chromem-go scale test.
//
// 50,000 random 768-dim vectors + 100 query vectors. Measures upsert time,
// RSS-ish memory via runtime.ReadMemStats, per-query top-10 latency
// (P50/P95/P99). Writes results to bench/results/chromem.json.
//
// Gate: P95 < 200ms, RAM < 4 GB.
//
// Run:
//
//	go run -tags=bench_chromem ./bench_chromem.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/philippgille/chromem-go"
)

const (
	nVectors = 50_000
	nQueries = 100
	dim      = 768
	topK     = 10
)

type chromemResult struct {
	Benchmark        string  `json:"benchmark"`
	NVectors         int     `json:"n_vectors"`
	NQueries         int     `json:"n_queries"`
	Dim              int     `json:"dim"`
	TopK             int     `json:"top_k"`
	UpsertSeconds    float64 `json:"upsert_seconds"`
	RAMBytesAfterIns uint64  `json:"ram_bytes_after_insert"`
	RAMBytesAfterQry uint64  `json:"ram_bytes_after_query"`
	RAMGBAfterQry    float64 `json:"ram_gb_after_query"`
	P50Ms            float64 `json:"p50_ms"`
	P95Ms            float64 `json:"p95_ms"`
	P99Ms            float64 `json:"p99_ms"`
	MinMs            float64 `json:"min_ms"`
	MaxMs            float64 `json:"max_ms"`
	Gate             string  `json:"gate"`
	GateP95LtMs      float64 `json:"gate_p95_lt_ms"`
	GateRAMLtGB      float64 `json:"gate_ram_lt_gb"`
	Notes            string  `json:"notes,omitempty"`
}

func randVec(r *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	var norm float64
	for i := 0; i < d; i++ {
		v[i] = float32(r.NormFloat64())
		norm += float64(v[i]) * float64(v[i])
	}
	// L2-normalize (cosine is the default similarity in chromem)
	if norm > 0 {
		s := float32(1.0 / sqrtFloat64(norm))
		for i := 0; i < d; i++ {
			v[i] *= s
		}
	}
	return v
}

func sqrtFloat64(x float64) float64 {
	// Avoid math import dependency circus; use Newton iteration (good enough)
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 12; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func main() {
	outDir := "results"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir results: %v\n", err)
		os.Exit(1)
	}
	outPath := filepath.Join(outDir, "chromem.json")

	fmt.Printf("Bench 1 (chromem): %d vectors × %d dim + %d queries × top-%d\n", nVectors, dim, nQueries, topK)

	r := rand.New(rand.NewSource(42))

	// ---- Init DB (in-memory, no persistence) ----
	db := chromem.NewDB()
	// We provide pre-computed vectors; chromem uses Document.Embedding directly
	// and only calls the embed func when Embedding is empty — never here.
	embedStub := func(ctx context.Context, text string) ([]float32, error) {
		return nil, fmt.Errorf("embed func should not be called (pre-embedded docs)")
	}
	coll, err := db.CreateCollection("bench", nil, embedStub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CreateCollection: %v\n", err)
		os.Exit(1)
	}

	// ---- Upsert 50k vectors ----
	ctx := context.Background()
	docs := make([]chromem.Document, 0, nVectors)
	for i := 0; i < nVectors; i++ {
		docs = append(docs, chromem.Document{
			ID:        fmt.Sprintf("doc-%d", i),
			Metadata:  map[string]string{"lang": "py", "idx": fmt.Sprintf("%d", i)},
			Embedding: randVec(r, dim),
			Content:   "", // no content — we're benching vector ops
		})
	}

	startIns := time.Now()
	// AddDocuments does concurrent embedding (but we pre-embedded via Identity func)
	if err := coll.AddDocuments(ctx, docs, runtime.NumCPU()); err != nil {
		fmt.Fprintf(os.Stderr, "AddDocuments: %v\n", err)
		os.Exit(1)
	}
	upsertSec := time.Since(startIns).Seconds()
	fmt.Printf("Upsert: %.2fs (%.0f docs/sec)\n", upsertSec, float64(nVectors)/upsertSec)

	runtime.GC()
	var msIns runtime.MemStats
	runtime.ReadMemStats(&msIns)
	fmt.Printf("HeapAlloc after insert: %.2f GB\n", float64(msIns.HeapAlloc)/(1<<30))

	// ---- 100 queries × top-10 ----
	queries := make([][]float32, nQueries)
	for i := 0; i < nQueries; i++ {
		queries[i] = randVec(r, dim)
	}

	latenciesMs := make([]float64, 0, nQueries)
	for i, q := range queries {
		t0 := time.Now()
		_, err := coll.QueryEmbedding(ctx, q, topK, nil, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "QueryEmbedding[%d]: %v\n", i, err)
			os.Exit(1)
		}
		latenciesMs = append(latenciesMs, float64(time.Since(t0).Microseconds())/1000.0)
	}

	sort.Float64s(latenciesMs)
	p50 := percentile(latenciesMs, 0.50)
	p95 := percentile(latenciesMs, 0.95)
	p99 := percentile(latenciesMs, 0.99)
	minMs := latenciesMs[0]
	maxMs := latenciesMs[len(latenciesMs)-1]

	runtime.GC()
	var msQry runtime.MemStats
	runtime.ReadMemStats(&msQry)

	ramGB := float64(msQry.HeapAlloc) / (1 << 30)

	gate := "PASS"
	notes := ""
	if p95 >= 200 {
		gate = "FAIL"
		notes += fmt.Sprintf("P95=%.1fms ≥ 200ms; ", p95)
	}
	if ramGB >= 4 {
		gate = "FAIL"
		notes += fmt.Sprintf("RAM=%.2fGB ≥ 4GB; ", ramGB)
	}

	res := chromemResult{
		Benchmark:        "chromem-go scale",
		NVectors:         nVectors,
		NQueries:         nQueries,
		Dim:              dim,
		TopK:             topK,
		UpsertSeconds:    upsertSec,
		RAMBytesAfterIns: msIns.HeapAlloc,
		RAMBytesAfterQry: msQry.HeapAlloc,
		RAMGBAfterQry:    ramGB,
		P50Ms:            p50,
		P95Ms:            p95,
		P99Ms:            p99,
		MinMs:            minMs,
		MaxMs:            maxMs,
		Gate:             gate,
		GateP95LtMs:      200,
		GateRAMLtGB:      4,
		Notes:            notes,
	}

	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
		os.Exit(1)
	}

	fmt.Printf("P50=%.1fms P95=%.1fms P99=%.1fms min=%.1f max=%.1f\n", p50, p95, p99, minMs, maxMs)
	fmt.Printf("Gate: %s %s\n", gate, notes)
	fmt.Printf("Wrote %s\n", outPath)
}
