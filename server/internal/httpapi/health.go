package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthHandler mirrors api/app/routers/health.py: returns {"status":"ok"}.
// Unauthenticated — used by probes.
//
// m6 — the probe now verifies the DB is reachable within 1 second. A stuck
// SQLite file (e.g. a locked WAL writer or a full disk) surfaces as HTTP 503
// instead of a silently-healthy 200.
func healthHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB != nil {
			pingCtx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			if err := d.DB.PingContext(pingCtx); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"status": "unhealthy",
					"reason": "db unreachable",
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

// statusHandler mirrors api/app/routers/health.py:status().
// m5 — model_loaded reflects the actual embeddings service state rather than
// being hard-coded to true; this way operators can see when the sidecar is
// still warming up or has crashed.
func statusHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectCount := 0
		activeJobs := 0

		if d.DB != nil {
			_ = d.DB.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM projects`).Scan(&projectCount)
			_ = d.DB.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM index_runs WHERE status = 'running'`).Scan(&activeJobs)
		}

		modelLoaded := false
		if d.EmbeddingSvc != nil {
			readyCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			modelLoaded = d.EmbeddingSvc.Ready(readyCtx) == nil
			cancel()
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":               "ok",
			"backend":              d.Backend,
			"server_version":       d.ServerVersion,
			"api_version":          d.APIVersion,
			"model_loaded":         modelLoaded,
			"embedding_model":      d.EmbeddingModel,
			"projects":             projectCount,
			"active_indexing_jobs": activeJobs,
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
