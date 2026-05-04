// admin_server.go holds the PR-E "Server" admin handlers: runtime config,
// sidecar restart/status, GGUF cache enumeration. Auth: every handler routes
// through mustBeAdmin → 403 for non-admin sessions / API keys.
//
// Wire format: hand-written payload structs (not the openapi.gen ones) so
// we can stamp time.Time as RFC3339Nano and emit map[string]string for the
// per-field source label without fighting the generator's nullable handling.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/runtimecfg"

	"github.com/google/uuid"
)

// runtimeConfigPayload is the JSON shape of GET/PUT /admin/runtime-config.
// Matches openapi.RuntimeConfig but is hand-written so updated_at uses the
// project-wide RFC3339Nano stamp and source values stay raw strings (the
// generated enum type would force a layer of conversion at no benefit).
type runtimeConfigPayload struct {
	EmbeddingModel          string                  `json:"embedding_model"`
	LlamaCtxSize            int                     `json:"llama_ctx_size"`
	LlamaNGpuLayers         int                     `json:"llama_n_gpu_layers"`
	LlamaNThreads           int                     `json:"llama_n_threads"`
	MaxEmbeddingConcurrency int                     `json:"max_embedding_concurrency"`
	LlamaBatchSize          int                     `json:"llama_batch_size"`
	Source                  map[string]string       `json:"source"`
	Recommended             *recommendedSnapshotPayload `json:"recommended,omitempty"`
	UpdatedAt               *string                 `json:"updated_at,omitempty"`
	UpdatedBy               *string                 `json:"updated_by,omitempty"`
}

type recommendedSnapshotPayload struct {
	EmbeddingModel          string `json:"embedding_model"`
	LlamaCtxSize            int    `json:"llama_ctx_size"`
	LlamaNGpuLayers         int    `json:"llama_n_gpu_layers"`
	LlamaNThreads           int    `json:"llama_n_threads"`
	MaxEmbeddingConcurrency int    `json:"max_embedding_concurrency"`
	LlamaBatchSize          int    `json:"llama_batch_size"`
}

func snapshotToPayload(snap runtimecfg.Snapshot, rec runtimecfg.Snapshot) runtimeConfigPayload {
	out := runtimeConfigPayload{
		EmbeddingModel:          snap.EmbeddingModel,
		LlamaCtxSize:            snap.LlamaCtxSize,
		LlamaNGpuLayers:         snap.LlamaNGpuLayers,
		LlamaNThreads:           snap.LlamaNThreads,
		MaxEmbeddingConcurrency: snap.MaxEmbeddingConcurrency,
		LlamaBatchSize:          snap.LlamaBatchSize,
		Source:                  snap.Source,
		Recommended: &recommendedSnapshotPayload{
			EmbeddingModel:          rec.EmbeddingModel,
			LlamaCtxSize:            rec.LlamaCtxSize,
			LlamaNGpuLayers:         rec.LlamaNGpuLayers,
			LlamaNThreads:           rec.LlamaNThreads,
			MaxEmbeddingConcurrency: rec.MaxEmbeddingConcurrency,
			LlamaBatchSize:          rec.LlamaBatchSize,
		},
	}
	if !snap.UpdatedAt.IsZero() {
		stamp := snap.UpdatedAt.UTC().Format(time.RFC3339Nano)
		out.UpdatedAt = &stamp
	}
	if snap.UpdatedBy != "" {
		v := snap.UpdatedBy
		out.UpdatedBy = &v
	}
	return out
}

// GetRuntimeConfig — GET /api/v1/admin/runtime-config (admin only).
func (s *Server) GetRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.Deps.RuntimeCfg == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime config not available")
		return
	}
	snap, err := s.Deps.RuntimeCfg.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load runtime config")
		return
	}
	writeJSON(w, http.StatusOK, snapshotToPayload(snap, s.Deps.RuntimeCfg.Recommended()))
}

// PutRuntimeConfig — PUT /api/v1/admin/runtime-config (admin only).
//
// The request body is a partial patch. Pointers tell us "this field was
// supplied"; the value tells us what to do with it (zero = clear override,
// non-zero = set override). nil pointers leave the existing override alone.
func (s *Server) PutRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	if s.Deps.RuntimeCfg == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime config not available")
		return
	}

	var body struct {
		EmbeddingModel          *string `json:"embedding_model"`
		LlamaCtxSize            *int    `json:"llama_ctx_size"`
		LlamaNGpuLayers         *int    `json:"llama_n_gpu_layers"`
		LlamaNThreads           *int    `json:"llama_n_threads"`
		MaxEmbeddingConcurrency *int    `json:"max_embedding_concurrency"`
		LlamaBatchSize          *int    `json:"llama_batch_size"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}

	// Light validation: refuse obviously broken numeric values. Negative
	// n_gpu_layers other than -1 is also broken (only -1 is the "all layers"
	// sentinel), but we let it through and trust llama-server to error at
	// spawn time — the supervisor surfaces that via SidecarStatus.last_error.
	if body.LlamaCtxSize != nil && *body.LlamaCtxSize < 0 {
		writeError(w, http.StatusUnprocessableEntity, "llama_ctx_size must be >= 0")
		return
	}
	if body.LlamaNThreads != nil && *body.LlamaNThreads < 0 {
		writeError(w, http.StatusUnprocessableEntity, "llama_n_threads must be >= 0")
		return
	}
	if body.MaxEmbeddingConcurrency != nil && *body.MaxEmbeddingConcurrency < 0 {
		writeError(w, http.StatusUnprocessableEntity, "max_embedding_concurrency must be >= 0")
		return
	}
	if body.LlamaBatchSize != nil && *body.LlamaBatchSize < 0 {
		writeError(w, http.StatusUnprocessableEntity, "llama_batch_size must be >= 0")
		return
	}

	patch := runtimecfg.Patch{
		EmbeddingModel:          body.EmbeddingModel,
		LlamaCtxSize:            body.LlamaCtxSize,
		LlamaNGpuLayers:         body.LlamaNGpuLayers,
		LlamaNThreads:           body.LlamaNThreads,
		MaxEmbeddingConcurrency: body.MaxEmbeddingConcurrency,
		LlamaBatchSize:          body.LlamaBatchSize,
	}
	updatedBy := ""
	if ac != nil {
		updatedBy = ac.User.Email
	}
	if err := s.Deps.RuntimeCfg.Set(r.Context(), patch, updatedBy); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save runtime config")
		return
	}
	snap, err := s.Deps.RuntimeCfg.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saved but could not reload runtime config")
		return
	}
	writeJSON(w, http.StatusOK, snapshotToPayload(snap, s.Deps.RuntimeCfg.Recommended()))
}

// ---------------------------------------------------------------------------
// Sidecar restart + status
// ---------------------------------------------------------------------------

// restartTracker holds in-flight restart state for the sidecar. PR-E V1: a
// single global flag is enough — only one restart can run at a time anyway
// because Service.Restart drains then mutates singleton supervisor state.
// Future versions may key by restart_id when we surface progress.
var restartInFlight atomic.Bool

// RestartSidecar — POST /api/v1/admin/sidecar/restart (admin only).
//
// Returns 202 immediately and runs the actual stop/spawn cycle on a goroutine
// so the HTTP request doesn't block for tens of seconds while the sidecar
// drains, terminates, and respawns. The dashboard polls GET /sidecar/status
// to observe the running → restarting → running transition.
func (s *Server) RestartSidecar(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.Deps.EmbeddingSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "embeddings service not available")
		return
	}
	embedSvc, ok := s.Deps.EmbeddingSvc.(*embeddings.Service)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "embeddings service does not support restart")
		return
	}
	if s.Deps.RuntimeCfg == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime config not available")
		return
	}

	if !restartInFlight.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "another restart is already in progress")
		return
	}

	id := uuid.NewString()
	go func() {
		defer restartInFlight.Store(false)
		// Resolve the latest config snapshot and apply onto a fresh shallow
		// copy of the env config so the supervisor sees the runtime overrides.
		bg, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		snap, err := s.Deps.RuntimeCfg.Get(bg)
		if err != nil {
			s.Deps.Logger.Error("sidecar restart: load runtime config", "err", err, "restart_id", id)
			return
		}
		// We don't have a clean handle on the original *config.Config in
		// admin_server.go; the embeddings.Service holds a pointer to the
		// process-wide one and Restart mutates it in place. snapshot.ApplyTo
		// rewrites the relevant fields on whatever cfg the embedSvc carries.
		snap.ApplyTo(embedSvc.Config())
		if err := embedSvc.Restart(bg, embedSvc.Config()); err != nil {
			s.Deps.Logger.Error("sidecar restart failed", "err", err, "restart_id", id)
			return
		}
		s.Deps.Logger.Info("sidecar restart complete", "restart_id", id)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"restart_id": id})
}

// GetSidecarStatus — GET /api/v1/admin/sidecar/status (admin only).
func (s *Server) GetSidecarStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.Deps.EmbeddingSvc == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"state":     "disabled",
			"ready":     false,
			"in_flight": 0,
		})
		return
	}
	embedSvc, ok := s.Deps.EmbeddingSvc.(*embeddings.Service)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"state":     "running",
			"ready":     true,
			"in_flight": 0,
		})
		return
	}
	st := embedSvc.Status()

	body := map[string]any{
		"state":             st.State,
		"ready":             st.Ready,
		"in_flight":         st.InFlight,
		"restart_in_flight": restartInFlight.Load(),
	}
	if st.PID > 0 {
		body["pid"] = st.PID
	}
	if st.Uptime > 0 {
		body["uptime_seconds"] = int(st.Uptime.Seconds())
	}
	if st.Model != "" {
		body["model"] = st.Model
	}
	if st.LastError != "" {
		body["last_error"] = st.LastError
	}
	// Active restart wins over the snapshot's transient state — Status() may
	// see a momentary "running" while the goroutine is still tearing down.
	if restartInFlight.Load() {
		body["state"] = "restarting"
	}
	writeJSON(w, http.StatusOK, body)
}

// ---------------------------------------------------------------------------
// Cached GGUF model enumeration
// ---------------------------------------------------------------------------

// ListModels — GET /api/v1/admin/models (admin only).
//
// Walks CIX_GGUF_CACHE_DIR/<safe_repo>/*.gguf (the layout DownloadGGUF uses)
// and returns one entry per .gguf file. Repo IDs are reconstructed from the
// directory name (we encode HF "owner/model" as "owner__model" to stay
// filesystem-safe).
func (s *Server) ListModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	cacheDir := embeddings.CacheDirFromService(s.Deps.EmbeddingSvc)
	if cacheDir == "" {
		// Service might be disabled / fake in tests — fall through to an
		// empty list with no cache_dir (UI shows free-text fallback).
		writeJSON(w, http.StatusOK, map[string]any{
			"models":    []any{},
			"cache_dir": "",
		})
		return
	}
	type entry struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	}
	out := []entry{}

	repos, err := os.ReadDir(cacheDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Deps.Logger.Warn("list models: read cache dir", "dir", cacheDir, "err", err)
	}
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		repoDir := filepath.Join(cacheDir, repo.Name())
		files, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".gguf") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			id := strings.Replace(repo.Name(), "__", "/", 1)
			out = append(out, entry{
				ID:        id,
				Path:      filepath.Join(repoDir, f.Name()),
				SizeBytes: info.Size(),
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models":    out,
		"cache_dir": cacheDir,
	})
}
