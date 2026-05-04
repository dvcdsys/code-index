// Package runtimecfg owns the dashboard-overridable subset of cix-server
// configuration. It sits one layer above package config (which is env-only,
// immutable, loaded once at boot). Resolution order for every field:
//
//	db value (runtime_settings row, set via dashboard) →
//	env value (CIX_* loaded into config.Config at boot) →
//	hardcoded recommended default
//
// A Snapshot also carries a Source map so the dashboard can render a "DB" /
// "Env" / "Recommended" pill next to each field, telling the admin where the
// current value came from. Set replaces the row wholesale (UPSERT id=1) so
// "clear this override" is just sending NULL.
package runtimecfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
)

// Source labels the origin of a resolved value; emitted in Snapshot.Source so
// the UI can render a "DB" / "Env" / "Recommended" pill next to each field.
const (
	SourceDB          = "db"
	SourceEnv         = "env"
	SourceRecommended = "recommended"
)

// FieldEmbeddingModel and friends name the keys in Snapshot.Source. Exported
// so handler/UI code can iterate without copy-pasting string literals.
const (
	FieldEmbeddingModel          = "embedding_model"
	FieldLlamaCtxSize            = "llama_ctx_size"
	FieldLlamaNGpuLayers         = "llama_n_gpu_layers"
	FieldLlamaNThreads           = "llama_n_threads"
	FieldMaxEmbeddingConcurrency = "max_embedding_concurrency"
	FieldLlamaBatchSize          = "llama_batch_size"
)

// Snapshot is a fully-resolved runtime config — every field is populated, no
// pointers / nullables — and the Source map records where each value came
// from. Returned by Get; consumed by main when wiring the embeddings service.
type Snapshot struct {
	EmbeddingModel          string
	LlamaCtxSize            int
	LlamaNGpuLayers         int
	LlamaNThreads           int
	MaxEmbeddingConcurrency int
	LlamaBatchSize          int

	// Source maps Field* constants to one of SourceDB/SourceEnv/SourceRecommended.
	Source map[string]string

	// UpdatedAt is the runtime_settings.updated_at value when at least one
	// field came from DB; zero otherwise.
	UpdatedAt time.Time
	// UpdatedBy is the runtime_settings.updated_by value when at least one
	// field came from DB; empty otherwise.
	UpdatedBy string
}

// Patch carries the new values an admin wants to write. nil pointers mean
// "don't change this field" — including "clear the DB override and fall back
// to env / recommended". To clear, send a non-nil pointer to the zero value
// for numeric fields, or a non-nil pointer to "" for the model. The handler
// does the nil/non-nil discrimination from the JSON request body.
type Patch struct {
	EmbeddingModel          *string
	LlamaCtxSize            *int
	LlamaNGpuLayers         *int
	LlamaNThreads           *int
	MaxEmbeddingConcurrency *int
	LlamaBatchSize          *int
}

// Service resolves runtime config from the DB, falling through to env-loaded
// config, then to hardcoded recommended values. Safe for concurrent use; the
// underlying *sql.DB pool is the only shared mutable state.
type Service struct {
	db  *sql.DB
	env *config.Config
}

// New constructs a Service. env is the bootstrap config.Config loaded at
// startup; runtimecfg never mutates it (the env layer is immutable by
// design).
func New(db *sql.DB, env *config.Config) *Service {
	return &Service{db: db, env: env}
}

// Recommended returns the hardcoded fallback Snapshot. These values are the
// project-wide "sensible defaults" — what we'd pick on a clean install with
// no env overrides and no DB row. Used by the UI to show "(Recommended)"
// alongside the current value.
func (s *Service) Recommended() Snapshot {
	defaultGpu := 0
	if runtime.GOOS == "darwin" {
		defaultGpu = -1
	}
	return Snapshot{
		EmbeddingModel:          "awhiteside/CodeRankEmbed-Q8_0-GGUF",
		LlamaCtxSize:            2048,
		LlamaNGpuLayers:         defaultGpu,
		LlamaNThreads:           runtime.NumCPU() / 2,
		MaxEmbeddingConcurrency: 5,
		LlamaBatchSize:          2048,
		Source:                  map[string]string{},
	}
}

type dbRow struct {
	embeddingModel          sql.NullString
	llamaCtxSize            sql.NullInt64
	llamaNGpuLayers         sql.NullInt64
	llamaNThreads           sql.NullInt64
	maxEmbeddingConcurrency sql.NullInt64
	llamaBatchSize          sql.NullInt64
	updatedAt               sql.NullString
	updatedBy               sql.NullString
}

func (s *Service) loadRow(ctx context.Context) (dbRow, bool, error) {
	var r dbRow
	err := s.db.QueryRowContext(ctx, `
		SELECT embedding_model, llama_ctx_size, llama_n_gpu_layers,
		       llama_n_threads, max_embedding_concurrency, llama_batch_size,
		       updated_at, updated_by
		FROM runtime_settings WHERE id = 1
	`).Scan(
		&r.embeddingModel, &r.llamaCtxSize, &r.llamaNGpuLayers,
		&r.llamaNThreads, &r.maxEmbeddingConcurrency, &r.llamaBatchSize,
		&r.updatedAt, &r.updatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return dbRow{}, false, nil
	}
	if err != nil {
		return dbRow{}, false, fmt.Errorf("select runtime_settings: %w", err)
	}
	return r, true, nil
}

// Get resolves the current effective Snapshot. Always returns a populated
// Snapshot; a nil DB row simply means every field falls through to env /
// recommended.
func (s *Service) Get(ctx context.Context) (Snapshot, error) {
	row, hasRow, err := s.loadRow(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	rec := s.Recommended()
	out := Snapshot{Source: map[string]string{}}

	// String — embedding model.
	switch {
	case hasRow && row.embeddingModel.Valid && row.embeddingModel.String != "":
		out.EmbeddingModel = row.embeddingModel.String
		out.Source[FieldEmbeddingModel] = SourceDB
	case s.env != nil && s.env.EmbeddingModel != "":
		out.EmbeddingModel = s.env.EmbeddingModel
		out.Source[FieldEmbeddingModel] = SourceEnv
	default:
		out.EmbeddingModel = rec.EmbeddingModel
		out.Source[FieldEmbeddingModel] = SourceRecommended
	}

	// Numeric fields — pull env value with fallback so the helper handles all
	// three layers uniformly. >0 from DB always wins; 0 == "unset".
	out.LlamaCtxSize = resolveInt(row.llamaCtxSize, hasRow, envIntOrZero(s.env, "ctx"), rec.LlamaCtxSize, &out.Source, FieldLlamaCtxSize)
	out.LlamaNGpuLayers = resolveIntSigned(row.llamaNGpuLayers, hasRow, envIntOrSentinel(s.env, "gpu"), rec.LlamaNGpuLayers, &out.Source, FieldLlamaNGpuLayers)
	out.LlamaNThreads = resolveInt(row.llamaNThreads, hasRow, envIntOrZero(s.env, "threads"), rec.LlamaNThreads, &out.Source, FieldLlamaNThreads)
	out.MaxEmbeddingConcurrency = resolveInt(row.maxEmbeddingConcurrency, hasRow, envIntOrZero(s.env, "conc"), rec.MaxEmbeddingConcurrency, &out.Source, FieldMaxEmbeddingConcurrency)
	out.LlamaBatchSize = resolveInt(row.llamaBatchSize, hasRow, envIntOrZero(s.env, "batch"), rec.LlamaBatchSize, &out.Source, FieldLlamaBatchSize)

	if hasRow {
		if row.updatedAt.Valid {
			if t, err := time.Parse(time.RFC3339Nano, row.updatedAt.String); err == nil {
				out.UpdatedAt = t
			}
		}
		if row.updatedBy.Valid {
			out.UpdatedBy = row.updatedBy.String
		}
	}
	return out, nil
}

// resolveInt picks DB > env > recommended for >0 fields. Used for ctx,
// threads, concurrency, batch — all where 0 is a sentinel meaning "unset".
func resolveInt(dbVal sql.NullInt64, hasRow bool, envVal int, recVal int, src *map[string]string, field string) int {
	if hasRow && dbVal.Valid && dbVal.Int64 > 0 {
		(*src)[field] = SourceDB
		return int(dbVal.Int64)
	}
	if envVal > 0 {
		(*src)[field] = SourceEnv
		return envVal
	}
	(*src)[field] = SourceRecommended
	return recVal
}

// resolveIntSigned is the n_gpu_layers special case: -1 (Metal: all layers)
// and 0 (CPU-only) are both legitimate values, so we treat any *non-NULL* DB
// row as authoritative. Env is authoritative whenever it differs from the
// platform default; recommended is the final fallback.
func resolveIntSigned(dbVal sql.NullInt64, hasRow bool, envVal int, recVal int, src *map[string]string, field string) int {
	if hasRow && dbVal.Valid {
		(*src)[field] = SourceDB
		return int(dbVal.Int64)
	}
	if envVal != recVal {
		(*src)[field] = SourceEnv
		return envVal
	}
	(*src)[field] = SourceRecommended
	return recVal
}

// envIntOrZero pulls a numeric value from the loaded env config or returns 0
// (the "unset" sentinel resolveInt understands). Centralised here so all
// callers route through one place — easier to keep in sync if config.Config
// gains more fields.
func envIntOrZero(env *config.Config, which string) int {
	if env == nil {
		return 0
	}
	switch which {
	case "ctx":
		return env.LlamaCtxSize
	case "threads":
		return env.LlamaNThreads
	case "conc":
		return env.MaxEmbeddingConcurrency
	case "batch":
		return env.LlamaBatchSize
	}
	return 0
}

// envIntOrSentinel mirrors envIntOrZero for fields where 0 is a legitimate
// value — currently only n_gpu_layers. Returns the env value verbatim.
func envIntOrSentinel(env *config.Config, which string) int {
	if env == nil {
		return 0
	}
	if which == "gpu" {
		return env.LlamaNGpuLayers
	}
	return 0
}

// Set applies a Patch to the runtime_settings row. nil pointer fields are
// preserved. Pointers to zero values (or empty strings) clear the override
// → next Get falls through to env / recommended for that field.
func (s *Service) Set(ctx context.Context, patch Patch, updatedBy string) error {
	row, hasRow, err := s.loadRow(ctx)
	if err != nil {
		return err
	}

	// Merge: start with current row (or empty), overlay patch.
	merged := row
	if patch.EmbeddingModel != nil {
		if *patch.EmbeddingModel == "" {
			merged.embeddingModel = sql.NullString{}
		} else {
			merged.embeddingModel = sql.NullString{String: *patch.EmbeddingModel, Valid: true}
		}
	}
	mergeInt(&merged.llamaCtxSize, patch.LlamaCtxSize)
	mergeInt(&merged.llamaNGpuLayers, patch.LlamaNGpuLayers)
	mergeInt(&merged.llamaNThreads, patch.LlamaNThreads)
	mergeInt(&merged.maxEmbeddingConcurrency, patch.MaxEmbeddingConcurrency)
	mergeInt(&merged.llamaBatchSize, patch.LlamaBatchSize)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if hasRow {
		_, err = s.db.ExecContext(ctx, `
			UPDATE runtime_settings
			SET embedding_model = ?, llama_ctx_size = ?, llama_n_gpu_layers = ?,
			    llama_n_threads = ?, max_embedding_concurrency = ?, llama_batch_size = ?,
			    updated_at = ?, updated_by = ?
			WHERE id = 1
		`,
			nullStr(merged.embeddingModel), nullInt(merged.llamaCtxSize),
			nullInt(merged.llamaNGpuLayers), nullInt(merged.llamaNThreads),
			nullInt(merged.maxEmbeddingConcurrency), nullInt(merged.llamaBatchSize),
			now, updatedBy,
		)
	} else {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO runtime_settings (
				id, embedding_model, llama_ctx_size, llama_n_gpu_layers,
				llama_n_threads, max_embedding_concurrency, llama_batch_size,
				updated_at, updated_by
			) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			nullStr(merged.embeddingModel), nullInt(merged.llamaCtxSize),
			nullInt(merged.llamaNGpuLayers), nullInt(merged.llamaNThreads),
			nullInt(merged.maxEmbeddingConcurrency), nullInt(merged.llamaBatchSize),
			now, updatedBy,
		)
	}
	if err != nil {
		return fmt.Errorf("upsert runtime_settings: %w", err)
	}
	return nil
}

// mergeInt overlays a patch field onto a NullInt64. nil patch keeps current;
// non-nil patch with zero clears (= NULL); non-nil non-zero sets.
func mergeInt(cur *sql.NullInt64, patch *int) {
	if patch == nil {
		return
	}
	if *patch == 0 {
		*cur = sql.NullInt64{}
		return
	}
	*cur = sql.NullInt64{Int64: int64(*patch), Valid: true}
}

func nullStr(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// ApplyTo merges the resolved Snapshot's settings onto a *config.Config so the
// rest of the server (embeddings supervisor, indexer, etc.) reads the
// effective values from one struct. Mutates env in place — callers usually
// pass a freshly-loaded copy at boot.
func (snap Snapshot) ApplyTo(env *config.Config) {
	if env == nil {
		return
	}
	env.EmbeddingModel = snap.EmbeddingModel
	env.LlamaCtxSize = snap.LlamaCtxSize
	env.LlamaNGpuLayers = snap.LlamaNGpuLayers
	env.LlamaNThreads = snap.LlamaNThreads
	env.MaxEmbeddingConcurrency = snap.MaxEmbeddingConcurrency
	env.LlamaBatchSize = snap.LlamaBatchSize
}
