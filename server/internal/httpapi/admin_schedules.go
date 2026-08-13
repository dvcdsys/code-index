package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/schedule"
)

// schedules returns the registry, or nil after writing a 503.
func (s *Server) schedules(w http.ResponseWriter) *schedule.Registry {
	if s.Deps.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable,
			"recurring tasks are not configured on this server")
		return nil
	}
	return s.Deps.Schedules
}

// ListSchedules — GET /api/v1/admin/schedules.
func (s *Server) ListSchedules(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	reg := s.schedules(w)
	if reg == nil {
		return
	}
	tasks, err := reg.List(r.Context())
	if err != nil {
		s.Deps.Logger.Error("list scheduled tasks", "err", err)
		writeError(w, http.StatusInternalServerError, "list scheduled tasks: "+err.Error())
		return
	}
	// Named field rather than a bare array: a top-level array cannot grow a
	// sibling later without breaking every client.
	writeJSON(w, http.StatusOK, struct {
		Tasks []schedule.State `json:"tasks"`
	}{Tasks: tasks})
}

// UpdateSchedule — PUT /api/v1/admin/schedules/{name}.
func (s *Server) UpdateSchedule(w http.ResponseWriter, r *http.Request, name string) {
	ac, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	reg := s.schedules(w)
	if reg == nil {
		return
	}

	var body struct {
		Cron    *string `json:"cron"`
		Enabled *bool   `json:"enabled"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}

	by := ""
	if ac != nil {
		by = ac.User.Email
	}
	st, err := reg.Save(r.Context(), name, body.Cron, body.Enabled, by)
	if err != nil {
		switch {
		case errors.Is(err, schedule.ErrUnknownTask):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, schedule.ErrInvalidCron):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			s.Deps.Logger.Error("save scheduled task", "task", name, "err", err)
			writeError(w, http.StatusInternalServerError, "save scheduled task: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, st)
}
