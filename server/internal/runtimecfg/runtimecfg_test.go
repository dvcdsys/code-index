package runtimecfg

import (
	"context"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/db"
)

func openTestDB(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	env := &config.Config{
		EmbeddingModel:          "env/model-a",
		LlamaCtxSize:            4096,
		LlamaNGpuLayers:         8,
		LlamaNThreads:           4,
		MaxEmbeddingConcurrency: 2,
		LlamaBatchSize:          1024,
	}
	return New(d, env)
}

// TestGet_NoRow_FallsThroughToEnv covers the "fresh install with env vars
// set, no dashboard overrides yet" path. Every field should be sourced from
// env and the Source map should reflect that uniformly.
func TestGet_NoRow_FallsThroughToEnv(t *testing.T) {
	svc := openTestDB(t)
	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.EmbeddingModel != "env/model-a" {
		t.Errorf("EmbeddingModel = %q, want env/model-a", got.EmbeddingModel)
	}
	if got.LlamaCtxSize != 4096 || got.LlamaNGpuLayers != 8 || got.LlamaNThreads != 4 {
		t.Errorf("numeric env fields not propagated: %+v", got)
	}
	for _, f := range []string{
		FieldEmbeddingModel, FieldLlamaCtxSize, FieldLlamaNGpuLayers,
		FieldLlamaNThreads, FieldMaxEmbeddingConcurrency, FieldLlamaBatchSize,
	} {
		if got.Source[f] != SourceEnv {
			t.Errorf("Source[%s] = %q, want env", f, got.Source[f])
		}
	}
}

// TestGet_FallsThroughToRecommended covers a clean DB + an empty env config.
// Every field should come from Recommended() and the Source map should say so.
func TestGet_FallsThroughToRecommended(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	svc := New(d, &config.Config{}) // env zero-valued

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec := svc.Recommended()
	if got.EmbeddingModel != rec.EmbeddingModel {
		t.Errorf("EmbeddingModel = %q, want %q", got.EmbeddingModel, rec.EmbeddingModel)
	}
	if got.LlamaCtxSize != rec.LlamaCtxSize {
		t.Errorf("LlamaCtxSize = %d, want %d", got.LlamaCtxSize, rec.LlamaCtxSize)
	}
	if got.Source[FieldEmbeddingModel] != SourceRecommended {
		t.Errorf("Source[model] = %q, want recommended", got.Source[FieldEmbeddingModel])
	}
}

// TestSet_Get_RoundTrip overlays a partial patch and verifies that:
//   - the patched field is now sourced from DB
//   - unpatched fields still come from env
//   - clearing (zero / "") returns the field to env source on next Get
func TestSet_Get_RoundTrip(t *testing.T) {
	svc := openTestDB(t)
	ctx := context.Background()

	model := "db/model-override"
	ctxSize := 8192
	if err := svc.Set(ctx, Patch{
		EmbeddingModel: &model,
		LlamaCtxSize:   &ctxSize,
	}, "tester"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EmbeddingModel != model || got.Source[FieldEmbeddingModel] != SourceDB {
		t.Errorf("model not from DB after Set: val=%q src=%q", got.EmbeddingModel, got.Source[FieldEmbeddingModel])
	}
	if got.LlamaCtxSize != ctxSize || got.Source[FieldLlamaCtxSize] != SourceDB {
		t.Errorf("ctx not from DB after Set: val=%d src=%q", got.LlamaCtxSize, got.Source[FieldLlamaCtxSize])
	}
	// Untouched field still env.
	if got.LlamaNThreads != 4 || got.Source[FieldLlamaNThreads] != SourceEnv {
		t.Errorf("untouched threads field shifted source: val=%d src=%q", got.LlamaNThreads, got.Source[FieldLlamaNThreads])
	}
	if got.UpdatedBy != "tester" {
		t.Errorf("UpdatedBy = %q, want tester", got.UpdatedBy)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be populated after Set")
	}

	// Clear the model override (empty string) — should return to env source.
	empty := ""
	if err := svc.Set(ctx, Patch{EmbeddingModel: &empty}, "tester"); err != nil {
		t.Fatalf("Set clear: %v", err)
	}
	got2, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got2.EmbeddingModel != "env/model-a" || got2.Source[FieldEmbeddingModel] != SourceEnv {
		t.Errorf("model didn't fall back to env after clear: val=%q src=%q", got2.EmbeddingModel, got2.Source[FieldEmbeddingModel])
	}
	// Other DB-set field (ctx) preserved.
	if got2.LlamaCtxSize != ctxSize || got2.Source[FieldLlamaCtxSize] != SourceDB {
		t.Errorf("ctx override lost during model clear: val=%d src=%q", got2.LlamaCtxSize, got2.Source[FieldLlamaCtxSize])
	}
}

// TestApplyTo verifies a Snapshot mutates *config.Config in-place so the
// rest of the server reads the effective values from one struct.
func TestApplyTo(t *testing.T) {
	snap := Snapshot{
		EmbeddingModel:          "x",
		LlamaCtxSize:            1,
		LlamaNGpuLayers:         2,
		LlamaNThreads:           3,
		MaxEmbeddingConcurrency: 4,
		LlamaBatchSize:          5,
	}
	cfg := &config.Config{EmbeddingModel: "old", LlamaCtxSize: 99}
	snap.ApplyTo(cfg)
	if cfg.EmbeddingModel != "x" || cfg.LlamaCtxSize != 1 || cfg.LlamaNGpuLayers != 2 ||
		cfg.LlamaNThreads != 3 || cfg.MaxEmbeddingConcurrency != 4 || cfg.LlamaBatchSize != 5 {
		t.Errorf("ApplyTo did not overwrite all fields: %+v", cfg)
	}
}
