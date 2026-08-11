// Package maintenance answers two admin questions about a running server:
// "what is it using?" and "how much of that is garbage I can drop?".
//
// It exists because the vector store is an in-memory database. chromem-go
// eagerly decodes every document of every collection at startup and never
// evicts, so a collection whose project was deleted from SQLite keeps its
// documents resident — for the life of the process — while being reachable by
// nothing. Deleting a project used to leave exactly that: the row went away,
// FK CASCADE took the chunks and symbols, and the vector collection stayed in
// RAM forever. The delete path now cleans up after itself (see
// projects.Artifacts), but every server that ran the old code has a backlog,
// and a cloned checkout or an abandoned provider namespace can still be
// orphaned by a crash mid-delete. This package finds that backlog and removes
// it on request.
//
// The split of responsibilities is deliberate: this package holds all the
// logic and touches the filesystem, the database and the vector store
// directly; internal/httpapi/admin_resources.go is a thin adapter that does
// auth, JSON and status codes. That is what makes the reconciliation testable
// against a t.TempDir() and an in-memory database without a router.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/jobs"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// CategoryID names one kind of reclaimable garbage. These strings are part of
// the HTTP contract (ReclaimCategory.id / CleanRequest.categories) — changing
// one breaks the dashboard.
type CategoryID string

const (
	// CatOrphanCollections is the only category that frees RAM as well as
	// disk: a chromem collection with no matching row in `projects`.
	CatOrphanCollections CategoryID = "orphan_collections"
	// CatOrphanRepos is a cloned checkout under <repos>/ whose project is gone.
	CatOrphanRepos CategoryID = "orphan_repos"
	// CatStaleNamespaces is a vector-store namespace directory belonging to a
	// provider/model that is no longer active. Disk only — chromem opens the
	// active namespace and nothing else, so these were never in RAM.
	CatStaleNamespaces CategoryID = "stale_namespaces"
	// CatStaleJobs is finished rows in the `jobs` queue, which has neither a
	// foreign key to projects nor any retention policy.
	CatStaleJobs CategoryID = "stale_jobs"
	// CatUnusedModels is a cached GGUF other than the active model. Off by
	// default: re-downloading one costs minutes and bandwidth.
	CatUnusedModels CategoryID = "unused_models"
)

// AllCategories is the canonical order categories are scanned and rendered in.
var AllCategories = []CategoryID{
	CatOrphanCollections,
	CatOrphanRepos,
	CatStaleNamespaces,
	CatStaleJobs,
	CatUnusedModels,
}

func validCategory(id CategoryID) bool {
	for _, c := range AllCategories {
		if c == id {
			return true
		}
	}
	return false
}

// Sentinel errors the HTTP adapter maps to status codes.
var (
	// ErrAnalysisExpired — the analysis_id is unknown or past its TTL.
	ErrAnalysisExpired = errors.New("maintenance: analysis expired or unknown")
	// ErrUnknownCategory — a category id the server does not recognise.
	ErrUnknownCategory = errors.New("maintenance: unknown category")
	// ErrNoCategories — clean was called with an empty selection.
	ErrNoCategories = errors.New("maintenance: no categories selected")
	// ErrAnalysisTimeout — the scan blew its budget. We fail closed rather
	// than return a partial picture: a truncated analysis that an admin then
	// cleans against is far worse than an error they can retry.
	ErrAnalysisTimeout = errors.New("maintenance: analysis timed out")
)

// Deps is everything the service needs. The three func fields are injected
// rather than taken as an *embeddings.Service so this package stays free of
// the embeddings/provider import graph and so tests can drive the
// active-provider answers directly. All of them may be nil.
type Deps struct {
	DB          *sql.DB
	Cfg         *config.Config
	VectorStore vectorstore.Maintainer
	Jobs        *jobs.Service
	Logger      *slog.Logger

	// ActiveChromaComponents returns the live provider's storage path
	// components (embeddings.Service.StoragePath). An empty result means
	// "unknown" and disables the stale-namespace category entirely — we never
	// guess which namespace is live.
	ActiveChromaComponents func() []string
	// ActiveGGUFCacheDir returns the GGUF cache directory, or "" when the
	// active provider has no local model (embeddings.CacheDirFromService).
	ActiveGGUFCacheDir func() string
	// ActiveModelRepo returns the HF repo id of the live embedding model,
	// resolved through runtime config so a dashboard-side model change counts.
	ActiveModelRepo func() string

	// Now is injectable so TTL and retention tests do not sleep.
	Now func() time.Time
}

// Service performs resource accounting and cleanup.
type Service struct {
	d     Deps
	cache *analysisCache
	usage *usageCache
}

// New returns a Service. Callers may construct one per request; the analysis
// cache is the only state, so it must be shared — build one at wiring time and
// keep it.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	return &Service{d: d, cache: newAnalysisCache(analysisTTL, d.Now), usage: &usageCache{}}
}

func (s *Service) now() time.Time { return s.d.Now() }

// Item is one reclaimable thing: a collection, a directory, a file, or (for
// stale_jobs) a whole set of rows collapsed into a single entry.
type Item struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Detail    string `json:"detail,omitempty"`
	SizeBytes int64  `json:"size_bytes"`

	// path is the absolute filesystem path to remove. Empty for stale_jobs,
	// which is a SQL delete. Unexported: it is an implementation detail of
	// Clean and must never reach the client.
	path string
	// collection is the raw chromem collection name for orphan_collections.
	collection string
}

// Category is one group of items plus the presentation metadata the dashboard
// renders. Labels and defaults live here rather than in React so the rules
// (what is destructive, what is pre-selected) are decided once, on the server.
type Category struct {
	ID                CategoryID `json:"id"`
	Label             string     `json:"label"`
	Description       string     `json:"description"`
	ItemCount         int        `json:"item_count"`
	SizeBytes         int64      `json:"size_bytes"`
	EstimatedRAMBytes int64      `json:"estimated_ram_bytes,omitempty"`
	DefaultSelected   bool       `json:"default_selected"`
	Destructive       bool       `json:"destructive"`
	// Items is the sample shown in the UI — at most maxItemsPerCategory.
	Items          []Item `json:"items"`
	ItemsTruncated bool   `json:"items_truncated"`

	// all is the complete item list. Clean iterates THIS, never Items:
	// deleting only what happened to fit in the browser sample would silently
	// leave the rest behind while reporting success.
	all []Item
}

// maxItemsPerCategory caps what we serialise. A server with 40k orphan
// directories should not push 40k rows into a browser; the counts and totals
// stay exact, only the sample is trimmed, and items_truncated says so.
const maxItemsPerCategory = 50

// Analysis is one snapshot of reclaimable garbage. It is handed to the client
// with an ID, which the client passes back to Clean.
//
// The ID is NOT a safety mechanism — every item is re-validated against live
// state immediately before it is deleted (see clean.go). It exists so the
// confirmation dialog and the delete act on the same described set, and so a
// stale confirmation fails cleanly instead of deleting something the admin
// never saw.
type Analysis struct {
	ID                    string     `json:"analysis_id"`
	GeneratedAt           time.Time  `json:"generated_at"`
	ExpiresAt             time.Time  `json:"expires_at"`
	DurationMS            int64      `json:"duration_ms"`
	TotalReclaimableBytes int64      `json:"total_reclaimable_bytes"`
	Categories            []Category `json:"categories"`
	Warnings              []string   `json:"warnings,omitempty"`
}

// DirSizeBytes walks dir and sums regular-file sizes. Returns (0,false) on any
// error (missing dir, permission, cancelled context) so callers can omit the
// number rather than report a misleading 0.
//
// The context is checked every so many entries: on a vector store that is one
// file per document these walks visit hundreds of thousands of entries, and a
// client that has already hung up should not be able to leave the server
// finishing the tree anyway.
//
// Lives here rather than in httpapi because both the resource endpoints and
// the project-detail card need it and there must be exactly one copy.
func DirSizeBytes(ctx context.Context, dir string) (int64, bool) {
	var total int64
	var seen int
	walkErr := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Checking every entry would make ctx.Err() a meaningful share of the
		// walk's cost; every 512 keeps cancellation prompt for free.
		if seen++; seen%512 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		return 0, false
	}
	return total, true
}

// dirSizeOrZero is the convenience form for places that already know the
// directory should exist and want a number to add up.
func dirSizeOrZero(ctx context.Context, dir string) int64 {
	n, _ := DirSizeBytes(ctx, dir)
	return n
}

// fileSizeOrZero stats one file.
func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// reposRoot is where cloned checkouts live. It must be derived exactly the way
// repocloner.LocalDirFor derives it — WorkspacesDataDir already defaults to
// ".../repos", and LocalDirFor appends "repos" again, so the real path has the
// segment twice. That is pre-existing and not ours to "fix" here: agreeing
// with the cloner matters far more than looking tidy, because disagreeing
// would mean either missing every orphan or, much worse, walking a directory
// full of live checkouts.
func (s *Service) reposRoot() string {
	if s.d.Cfg == nil || s.d.Cfg.WorkspacesDataDir == "" {
		return ""
	}
	return filepath.Join(s.d.Cfg.WorkspacesDataDir, "repos")
}

// ctxDone reports whether the context is finished, so scanners can bail out of
// long directory walks between items.
func ctxDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
