package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/maintenance"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// newMaintenanceService builds the maintenance service for this router. It is
// constructed once, at wiring time, because the analysis cache is the whole
// point: the id handed to the client by analyze has to still mean something
// when clean comes back with it.
//
// The three closures are how this stays decoupled from the embeddings package
// on the maintenance side; each one degrades to "unknown", which the scanners
// treat as "refuse to classify anything in this category" rather than as a
// licence to guess.
func newMaintenanceService(d Deps) *maintenance.Service {
	var vsm vectorstore.Maintainer
	if d.VectorStore != nil {
		if m, ok := d.VectorStore.(vectorstore.Maintainer); ok {
			vsm = m
		}
	}
	embedSvc, _ := d.EmbeddingSvc.(*embeddings.Service)

	return maintenance.New(maintenance.Deps{
		DB:          d.DB,
		Cfg:         d.Cfg,
		VectorStore: vsm,
		Jobs:        d.Jobs,
		Logger:      d.Logger,
		ActiveChromaComponents: func() []string {
			if embedSvc == nil {
				return nil
			}
			return embedSvc.StoragePath()
		},
		ActiveGGUFCacheDir: func() string {
			return embeddings.CacheDirFromService(d.EmbeddingSvc)
		},
		ActiveModelRepo: func() string {
			if embedSvc == nil {
				return ""
			}
			// The provider fingerprint is "<kind>:<model>"; the model half is
			// the HF repo id, which is what names the cache directory. Read
			// from the live provider rather than from config so a model
			// changed through the dashboard counts immediately — deleting the
			// model the server is currently running on would be the worst
			// possible outcome of a cleanup feature.
			id := embedSvc.EmbeddingModel()
			_, repo, found := strings.Cut(id, ":")
			if !found {
				return ""
			}
			return repo
		},
	})
}

// resourceService returns the shared maintenance service, or nil when this
// router has no config wired (router-only tests).
func (s *Server) resourceService(w http.ResponseWriter) *maintenance.Service {
	if s.Deps.Cfg == nil || s.maintenance == nil {
		writeError(w, http.StatusServiceUnavailable, "resource accounting is not configured on this server")
		return nil
	}
	return s.maintenance
}

// GetResourceUsage — GET /api/v1/admin/resources.
func (s *Server) GetResourceUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.resourceService(w)
	if svc == nil {
		return
	}
	writeJSON(w, http.StatusOK, svc.Usage(r.Context()))
}

// AnalyzeReclaimable — POST /api/v1/admin/resources/analyze.
func (s *Server) AnalyzeReclaimable(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	svc := s.resourceService(w)
	if svc == nil {
		return
	}
	analysis, err := svc.Analyze(r.Context())
	if err != nil {
		if errors.Is(err, maintenance.ErrAnalysisTimeout) {
			// Fail closed. A partial analysis is indistinguishable from a
			// complete one by the time it reaches a confirmation dialog.
			writeError(w, http.StatusServiceUnavailable,
				"analysis did not finish in time — the vector store or clone directory is unusually large")
			return
		}
		s.Deps.Logger.Error("analyze reclaimable", "err", err)
		writeError(w, http.StatusInternalServerError, "analyze: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, analysis)
}

// CleanResources — POST /api/v1/admin/resources/clean.
func (s *Server) CleanResources(w http.ResponseWriter, r *http.Request) {
	user, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	svc := s.resourceService(w)
	if svc == nil {
		return
	}

	var body struct {
		AnalysisID string   `json:"analysis_id"`
		Categories []string `json:"categories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	cats := make([]maintenance.CategoryID, 0, len(body.Categories))
	for _, c := range body.Categories {
		cats = append(cats, maintenance.CategoryID(c))
	}

	// mustBeAdmin returns a nil authContext when CIX_AUTH_DISABLED=true, which
	// is a legitimate deployment mode — guard before dereferencing.
	actor := "auth-disabled"
	if user != nil {
		actor = user.User.Email
	}
	s.Deps.Logger.Info("maintenance clean requested",
		"actor", actor, "analysis_id", body.AnalysisID, "categories", body.Categories)

	res, err := svc.Clean(r.Context(), body.AnalysisID, cats)
	switch {
	case errors.Is(err, maintenance.ErrAnalysisExpired):
		writeError(w, http.StatusConflict, "that analysis has expired or was already used — run Analyze again")
		return
	case errors.Is(err, maintenance.ErrNoCategories):
		writeError(w, http.StatusUnprocessableEntity, "select at least one category to clean")
		return
	case errors.Is(err, maintenance.ErrUnknownCategory):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		s.Deps.Logger.Error("maintenance clean", "err", err)
		writeError(w, http.StatusInternalServerError, "clean: "+err.Error())
		return
	}

	// 200 even when individual items failed. The per-category counts carry
	// that, and failing the whole call would hide everything that was
	// successfully reclaimed alongside the one directory we could not remove.
	s.Deps.Logger.Info("maintenance clean finished",
		"actor", actor, "reclaimed_bytes", res.ReclaimedBytes, "duration_ms", res.DurationMS)
	writeJSON(w, http.StatusOK, res)
}
