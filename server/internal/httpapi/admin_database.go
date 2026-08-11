package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/dbmaint"
)

// newDBMaintService builds the database-maintenance service for this router,
// or nil when there is no config to locate the database file with (router-only
// tests). Constructed once so the throughput measured by a compaction survives
// into the next estimate.
func newDBMaintService(d Deps) *dbmaint.Service {
	if d.Cfg == nil || d.DB == nil {
		return nil
	}
	return dbmaint.New(dbmaint.Deps{
		DB:     d.DB,
		DBPath: d.Cfg.SQLitePath,
		Logger: d.Logger,
	})
}

// databaseService returns the shared service, or nil after writing a 503.
func (s *Server) databaseService(w http.ResponseWriter) *dbmaint.Service {
	if s.dbmaint == nil {
		writeError(w, http.StatusServiceUnavailable,
			"database maintenance is not configured on this server")
		return nil
	}
	return s.dbmaint
}

// GetDatabaseState — GET /api/v1/admin/database.
func (s *Server) GetDatabaseState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.databaseService(w)
	if svc == nil {
		return
	}
	stats, err := svc.Stats(r.Context())
	if err != nil {
		s.Deps.Logger.Error("read database state", "err", err)
		writeError(w, http.StatusInternalServerError, "read database state: "+err.Error())
		return
	}

	// blocked_reason and operation are computed here rather than in the
	// service so the button state and the reason shown next to it can never
	// disagree — they come from one read.
	out := struct {
		dbmaint.Stats
		BlockedReason *string        `json:"blocked_reason"`
		Operation     *dbmaint.State `json:"operation"`
	}{Stats: stats}

	if op := svc.Status(); op.Phase != dbmaint.PhaseIdle {
		out.Operation = &op
	}
	writeJSON(w, http.StatusOK, out)
}

// CheckpointWal — POST /api/v1/admin/database/checkpoint.
func (s *Server) CheckpointWal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.databaseService(w)
	if svc == nil {
		return
	}
	res, err := svc.Checkpoint(r.Context())
	if err != nil {
		s.Deps.Logger.Error("checkpoint wal", "err", err)
		writeError(w, http.StatusInternalServerError, "checkpoint: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ReclaimFreePages — POST /api/v1/admin/database/reclaim.
func (s *Server) ReclaimFreePages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.databaseService(w)
	if svc == nil {
		return
	}

	var body struct {
		MaxPages *int64 `json:"max_pages"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// An absent body is the documented way to ask for an unbounded reclaim,
	// so EOF is success; anything else is a malformed request.
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	var maxPages int64
	if body.MaxPages != nil {
		if *body.MaxPages < 1 {
			writeError(w, http.StatusUnprocessableEntity, "max_pages must be at least 1")
			return
		}
		maxPages = *body.MaxPages
	}

	res, err := svc.Reclaim(r.Context(), maxPages)
	if err != nil {
		if errors.Is(err, dbmaint.ErrNotIncremental) {
			writeError(w, http.StatusConflict,
				"this database cannot reclaim space incrementally — compact it once to switch it to incremental mode")
			return
		}
		s.Deps.Logger.Error("reclaim free pages", "err", err)
		writeError(w, http.StatusInternalServerError, "reclaim: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// GetMaintenanceStatus — GET /maintenance/status (public).
//
// Public on purpose. It reads a file beside the database and touches neither
// the database nor the session table, so it keeps answering while the server
// is restarting into a compacted file — the moment an admin is most likely to
// be watching. Anything authenticated would 401 there, and anything DB-backed
// would block.
func (s *Server) GetMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	if s.dbmaint == nil {
		// No maintenance wired is indistinguishable from nothing happening,
		// and this endpoint must never fail — a banner polling it treats an
		// error as "the server is down".
		writeJSON(w, http.StatusOK, dbmaint.State{Phase: dbmaint.PhaseIdle})
		return
	}
	writeJSON(w, http.StatusOK, s.dbmaint.Status())
}
