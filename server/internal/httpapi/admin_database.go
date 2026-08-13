package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/dvcdsys/code-index/server/internal/dbmaint"
)

// DBMaintHooks is how compaction reaches the parts of the process that live
// outside the router: the background services it has to stop, the write gate
// it has to close, and the restart that adopts the finished copy.
//
// They are hooks rather than direct references so the router keeps its current
// dependencies and so the whole mechanism can be exercised in a test without a
// job queue or a real process to restart.
type DBMaintHooks struct {
	// Service, when set, is used instead of building one. main supplies it so
	// the scheduler ticks on the *same* instance the handlers use — two
	// services would not share the "already running" flag, and a scheduled
	// compaction could start on top of a manual one.
	Service *dbmaint.Service
	// Gate is the write freeze consulted by the middleware, by the two Touch
	// call sites and by the health probe. Nil means never frozen.
	Gate *dbmaint.Gate
	// Quiesce stops background writers and waits for them to drain.
	Quiesce func(ctx context.Context) error
	// RequestRestart asks the process to shut down and re-execute itself.
	RequestRestart func()
}

// newDBMaintService builds the database-maintenance service for this router,
// or nil when there is no config to locate the database file with (router-only
// tests). Constructed once so the throughput measured by a compaction survives
// into the next estimate.
func newDBMaintService(d Deps) *dbmaint.Service {
	if d.DBMaint.Service != nil {
		return d.DBMaint.Service
	}
	if d.Cfg == nil || d.DB == nil {
		return nil
	}
	var freeze, thaw func()
	if d.DBMaint.Gate != nil {
		freeze, thaw = d.DBMaint.Gate.Freeze, d.DBMaint.Gate.Thaw
	}
	var sessions func() int
	if d.Indexer != nil {
		sessions = d.Indexer.ActiveSessions
	}
	return dbmaint.New(dbmaint.Deps{
		DB:             d.DB,
		DBPath:         d.Cfg.SQLitePath,
		Logger:         d.Logger,
		Quiesce:        d.DBMaint.Quiesce,
		RequestRestart: d.DBMaint.RequestRestart,
		Freeze:         freeze,
		Thaw:           thaw,
		ActiveJobs:     dbmaint.ActiveWorkCounter(d.DB, sessions),
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

	if reason := svc.BlockedReason(r.Context(), stats); reason != "" {
		out.BlockedReason = &reason
	}
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

// CompactDatabase — POST /api/v1/admin/database/compact.
//
// Returns 202 and does the work in the background. Everything that can refuse
// has already happened by the time this returns: a caller that gets an error
// is on a server that never left normal service, and one that gets a 202 is on
// a server that will restart.
func (s *Server) CompactDatabase(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.databaseService(w)
	if svc == nil {
		return
	}

	st, err := svc.Compact(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, dbmaint.ErrCompactionRunning):
			writeError(w, http.StatusConflict, "a database compaction is already running")
		case errors.Is(err, dbmaint.ErrJobsInFlight):
			writeError(w, http.StatusConflict,
				"an indexing or clone job is in flight — compaction takes the server read-only and would fail it mid-run")
		case errors.Is(err, dbmaint.ErrInsufficientDisk):
			writeError(w, http.StatusInsufficientStorage, err.Error())
		default:
			s.Deps.Logger.Error("start database compaction", "err", err)
			writeError(w, http.StatusInternalServerError, "compact: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// SetAutoVacuumMode — PUT /api/v1/admin/database/auto-vacuum.
//
// The reclaim mode is a setting, not an operation. Asking for the mode the
// database is already in does nothing and answers 200; only a real change
// costs the rebuild, and then this behaves exactly like compaction because it
// *is* compaction underneath.
func (s *Server) SetAutoVacuumMode(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.databaseService(w)
	if svc == nil {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}

	st, changed, err := svc.SetAutoVacuum(r.Context(), dbmaint.AutoVacuum(body.Mode))
	if err != nil {
		switch {
		case errors.Is(err, dbmaint.ErrInvalidSchedule):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, dbmaint.ErrCompactionRunning):
			writeError(w, http.StatusConflict, "a database compaction is already running")
		case errors.Is(err, dbmaint.ErrJobsInFlight):
			writeError(w, http.StatusConflict,
				"an indexing or clone job is in flight — changing the reclaim mode rebuilds the database and would fail it mid-run")
		case errors.Is(err, dbmaint.ErrInsufficientDisk):
			writeError(w, http.StatusInsufficientStorage, err.Error())
		default:
			s.Deps.Logger.Error("set auto-vacuum mode", "err", err)
			writeError(w, http.StatusInternalServerError, "set reclaim mode: "+err.Error())
		}
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, st)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
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
