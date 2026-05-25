package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/jobs"
)

type jobPayload struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	DedupeKey   *string        `json:"dedupe_key"`
	Payload     map[string]any `json:"payload"`
	Attempts    int            `json:"attempts"`
	MaxAttempts int            `json:"max_attempts"`
	LastError   *string        `json:"last_error"`
	ScheduledAt time.Time      `json:"scheduled_at"`
	StartedAt   *time.Time     `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at"`
	CreatedAt   time.Time      `json:"created_at"`
}

func jobToPayload(j jobs.Job) jobPayload {
	var dedupe *string
	if j.DedupeKey != "" {
		v := j.DedupeKey
		dedupe = &v
	}
	var lastErr *string
	if j.LastError != "" {
		v := j.LastError
		lastErr = &v
	}
	payload := map[string]any{}
	if len(j.Payload) > 0 {
		_ = json.Unmarshal(j.Payload, &payload)
	}
	return jobPayload{
		ID:          j.ID,
		Type:        j.Type,
		Status:      j.Status,
		DedupeKey:   dedupe,
		Payload:     payload,
		Attempts:    j.Attempts,
		MaxAttempts: j.MaxAttempts,
		LastError:   lastErr,
		ScheduledAt: j.ScheduledAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		CreatedAt:   j.CreatedAt,
	}
}

// ListJobs — GET /api/v1/jobs.
func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, params openapi.ListJobsParams) {
	if s.Deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs queue is not configured on this server")
		return
	}
	status := ""
	if params.Status != nil {
		status = string(*params.Status)
	}
	jobType := ""
	if params.Type != nil {
		jobType = *params.Type
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	list, err := s.Deps.Jobs.List(r.Context(), status, jobType, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list jobs")
		return
	}
	out := make([]jobPayload, 0, len(list))
	for _, j := range list {
		out = append(out, jobToPayload(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":  out,
		"total": len(out),
	})
}
