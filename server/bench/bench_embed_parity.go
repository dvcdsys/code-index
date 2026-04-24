//go:build bench_embed_parity

// Bench 3 — embedding parity between Python (llama-cpp-python) and Go (go-llama.cpp).
//
// Reference vectors come from `results/reference_embeddings.json`, produced by
// `emit_reference_embeddings.py` (run this in the api/ venv first). The Go
// side loads the same GGUF via go-llama.cpp, applies the same query prefix
// rule, and computes cosine similarity per phrase.
//
// Gate: mean cosine ≥ 0.999 AND min cosine ≥ 0.995 across all 10 phrases.
//
// Run:
//
//	# Step 1 (Python, with api/ venv):
//	python emit_reference_embeddings.py
//	# Step 2 (Go):
//	go run -tags=bench_embed_parity ./bench_embed_parity.go
//
// If `results/reference_embeddings.json` is missing (e.g. no Python env
// available and GGUF not cached locally) the bench exits with status BLOCKED
// and writes results/embed_parity.json with that status.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	llama "github.com/go-skynet/go-llama.cpp"
)

type refItem struct {
	Phrase          string    `json:"phrase"`
	IsQuery         bool      `json:"is_query"`
	TextSentToModel string    `json:"text_sent_to_model"`
	Vector          []float64 `json:"vector"`
}

type refFile struct {
	Model       string    `json:"model"`
	GGUFPath    string    `json:"gguf_path"`
	Dim         int       `json:"dim"`
	QueryPrefix string    `json:"query_prefix"`
	Items       []refItem `json:"items"`
}

type phraseResult struct {
	Phrase  string  `json:"phrase"`
	IsQuery bool    `json:"is_query"`
	Cosine  float64 `json:"cosine"`
	Gate    string  `json:"gate"`
}

type bench3Result struct {
	Benchmark   string         `json:"benchmark"`
	Model       string         `json:"model"`
	GGUFPath    string         `json:"gguf_path"`
	Dim         int            `json:"dim"`
	Phrases     []phraseResult `json:"phrases"`
	MeanCosine  float64        `json:"mean_cosine"`
	MinCosine   float64        `json:"min_cosine"`
	GateMin     float64        `json:"gate_min_cosine"`
	GateMean    float64        `json:"gate_mean_cosine"`
	Gate        string         `json:"gate"`
	Blocked     bool           `json:"blocked"`
	BlockReason string         `json:"block_reason,omitempty"`
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return math.NaN()
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return math.NaN()
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func writeBlocked(outPath, reason string) {
	res := bench3Result{
		Benchmark:   "embed parity Python vs Go",
		Gate:        "BLOCKED",
		Blocked:     true,
		BlockReason: reason,
		GateMean:    0.999,
		GateMin:     0.995,
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(outPath, b, 0o644)
	fmt.Printf("Gate: BLOCKED — %s\nWrote %s\n", reason, outPath)
}

func main() {
	outDir := "results"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir results: %v\n", err)
		os.Exit(1)
	}
	outPath := filepath.Join(outDir, "embed_parity.json")
	refPath := filepath.Join(outDir, "reference_embeddings.json")

	// ---- Load reference ----
	raw, err := os.ReadFile(refPath)
	if err != nil {
		writeBlocked(outPath, fmt.Sprintf("missing %s — run emit_reference_embeddings.py first", refPath))
		return
	}
	var ref refFile
	if err := json.Unmarshal(raw, &ref); err != nil {
		writeBlocked(outPath, fmt.Sprintf("parse reference: %v", err))
		return
	}
	if len(ref.Items) == 0 {
		writeBlocked(outPath, "reference has zero items")
		return
	}
	if _, err := os.Stat(ref.GGUFPath); err != nil {
		writeBlocked(outPath, fmt.Sprintf("GGUF not accessible at %s: %v", ref.GGUFPath, err))
		return
	}

	// ---- Init go-llama.cpp with embedding mode ----
	model, err := llama.New(
		ref.GGUFPath,
		llama.EnableEmbeddings,
		llama.SetContext(2048+128),
		llama.SetGPULayers(-1), // Metal on mac; CUDA on linux if built with it
	)
	if err != nil {
		writeBlocked(outPath, fmt.Sprintf("go-llama.cpp load failed: %v (document exact error, flag for Linux retry)", err))
		return
	}
	defer model.Free()

	// ---- Embed each phrase and compare ----
	phrases := make([]phraseResult, 0, len(ref.Items))
	var sum float64
	minCos := math.Inf(1)
	okCount := 0

	for _, it := range ref.Items {
		// NOTE: ref.TextSentToModel already has the query prefix if is_query.
		// We feed that exact string to ensure identical input.
		vec, err := model.Embeddings(it.TextSentToModel)
		if err != nil {
			phrases = append(phrases, phraseResult{Phrase: it.Phrase, IsQuery: it.IsQuery, Cosine: 0, Gate: "ERROR"})
			continue
		}
		// Convert []float32 -> []float64 for cosine
		goVec := make([]float64, len(vec))
		for i, v := range vec {
			goVec[i] = float64(v)
		}
		cos := cosine(it.Vector, goVec)
		g := "FAIL"
		if cos >= 0.999 {
			g = "PASS"
			okCount++
		} else if cos >= 0.995 {
			g = "MARGINAL"
		}
		phrases = append(phrases, phraseResult{Phrase: it.Phrase, IsQuery: it.IsQuery, Cosine: cos, Gate: g})
		sum += cos
		if cos < minCos {
			minCos = cos
		}
		fmt.Printf("  [%s] cos=%.6f %s\n", map[bool]string{true: "Q", false: " "}[it.IsQuery], cos, it.Phrase[:min(60, len(it.Phrase))])
	}

	mean := sum / float64(len(phrases))
	gate := "PASS"
	if mean < 0.999 || minCos < 0.995 {
		gate = "FAIL"
	}

	res := bench3Result{
		Benchmark:  "embed parity Python vs Go",
		Model:      ref.Model,
		GGUFPath:   ref.GGUFPath,
		Dim:        ref.Dim,
		Phrases:    phrases,
		MeanCosine: mean,
		MinCosine:  minCos,
		GateMean:   0.999,
		GateMin:    0.995,
		Gate:       gate,
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(outPath, b, 0o644)
	fmt.Printf("mean=%.6f min=%.6f Gate: %s\nWrote %s\n", mean, minCos, gate, outPath)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
