package httpapi

import (
	"encoding/json"
	"net/http"
)

// healthHandler mirrors api/app/routers/health.py: returns {"status":"ok"}.
// Unauthenticated — used by probes. We intentionally do not include a version
// here to stay byte-identical with the Python response.
func healthHandler(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

// statusHandler mirrors the shape of api/app/routers/health.py:status().
// Phase 1 does not enforce the API key yet — that middleware lands alongside
// the project/index routers in Phase 2+.
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

		writeJSON(w, http.StatusOK, map[string]any{
			"status":               "ok",
			"backend":              d.Backend,
			"server_version":       d.ServerVersion,
			"api_version":          d.APIVersion,
			"model_loaded":         true,
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
