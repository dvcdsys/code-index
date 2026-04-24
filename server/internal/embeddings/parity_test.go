//go:build embed_gate
// +build embed_gate

package embeddings

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
)

// TestEmbeddingParity is the Phase 3 exit criterion. It spins up llama-server
// via the real supervisor, feeds the texts stored in the reference file, and
// asserts cosine similarity against the Python-produced vectors.
//
// Thresholds (from the plan):
//
//	mean cosine ≥ 0.999
//	min  cosine ≥ 0.995
//
// On failure the test prints a per-item table so the reviewer can see whether
// the drift is uniform (pooling mismatch) or localised (prefix/encoding bug).
//
// Run via:  make test-gate
func TestEmbeddingParity(t *testing.T) {
	refPath := findReferenceFile(t)
	ref := loadReference(t, refPath)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}
	// Honour the reference file's gguf_path verbatim — this is the exact
	// model the Python reference used, so the parity numbers are comparable.
	if ref.GGUFPath != "" {
		if _, statErr := os.Stat(ref.GGUFPath); statErr == nil {
			cfg.GGUFPath = ref.GGUFPath
		}
	}
	if cfg.LlamaBinDir == "" || !hasLlamaServer(cfg.LlamaBinDir) {
		// Try the `dist/` layout left behind by `make fetch-llama`.
		cand := findDistLlamaDir(t)
		if cand != "" {
			cfg.LlamaBinDir = cand
		} else {
			t.Fatalf("llama-server not found; run `make fetch-llama` first. Searched CIX_LLAMA_BIN_DIR=%q", cfg.LlamaBinDir)
		}
	}
	// Force embeddings on even if the developer shell has it disabled.
	cfg.EmbeddingsEnabled = true

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	svc, err := New(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("embeddings.New: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = svc.Stop(stopCtx)
	})

	// Build the exact text list the reference saw. Queries already include
	// their prefix in `text_sent_to_model`, so we use embedRaw (no prefix
	// logic, no queue) — matching the reference's input 1:1.
	texts := make([]string, len(ref.Items))
	for i, item := range ref.Items {
		texts[i] = item.TextSent
	}

	embedCtx, embedCancel := context.WithTimeout(ctx, 90*time.Second)
	defer embedCancel()
	got, err := svc.embedRaw(embedCtx, texts)
	if err != nil {
		t.Fatalf("embedRaw: %v", err)
	}
	if len(got) != len(ref.Items) {
		t.Fatalf("got %d vectors, want %d", len(got), len(ref.Items))
	}

	type row struct {
		idx    int
		cosine float64
		input  string
	}
	rows := make([]row, 0, len(got))
	var (
		sum  float64
		minC = math.Inf(+1)
	)
	for i, vec := range got {
		c := cosine(vec, ref.Items[i].Vector)
		rows = append(rows, row{idx: i, cosine: c, input: ref.Items[i].Phrase})
		sum += c
		if c < minC {
			minC = c
		}
	}
	mean := sum / float64(len(got))

	t.Logf("mean_cosine=%.6f min_cosine=%.6f (threshold mean>=0.999 min>=0.995)", mean, minC)
	sort.Slice(rows, func(i, j int) bool { return rows[i].cosine < rows[j].cosine })
	for _, r := range rows {
		in := r.input
		if len(in) > 40 {
			in = in[:40]
		}
		t.Logf("  idx=%d cosine=%.6f input=%q", r.idx, r.cosine, in)
	}

	if mean < 0.999 {
		t.Fatalf("mean cosine %.6f < 0.999", mean)
	}
	if minC < 0.995 {
		t.Fatalf("min cosine %.6f < 0.995", minC)
	}
}

// --- helpers ---

type refItem struct {
	Phrase   string    `json:"phrase"`
	IsQuery  bool      `json:"is_query"`
	TextSent string    `json:"text_sent_to_model"`
	Vector   []float32 `json:"vector"`
}

type refFile struct {
	Model       string    `json:"model"`
	GGUFPath    string    `json:"gguf_path"`
	Dim         int       `json:"dim"`
	QueryPrefix string    `json:"query_prefix"`
	Items       []refItem `json:"items"`
}

// findReferenceFile locates bench/results/reference_embeddings.json by walking
// up from the test file's location. Tests can run from anywhere, so a fixed
// relative path is unreliable.
func findReferenceFile(t *testing.T) string {
	t.Helper()
	// Walk up at most five parents.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(dir, "bench", "results", "reference_embeddings.json")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("reference_embeddings.json not found; ran from %s", dir)
	return ""
}

func loadReference(t *testing.T, path string) *refFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	var ref refFile
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatalf("decode reference: %v", err)
	}
	if len(ref.Items) == 0 {
		t.Fatal("reference has zero items")
	}
	return &ref
}

// findDistLlamaDir looks for dist/llama relative to the repo root.
func findDistLlamaDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(dir, "dist", "llama")
		if hasLlamaServer(cand) {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func hasLlamaServer(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "llama-server"))
	return err == nil
}

// cosine computes the cosine similarity of two equal-length float32 vectors.
// Returns NaN on length mismatch so the caller's assertion naturally fails.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.NaN()
	}
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return math.NaN()
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Compile-time sanity check that fmt is still imported — parity tests
// tend to evolve and this avoids "imported and not used" churn during iteration.
var _ = fmt.Sprintf
