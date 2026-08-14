// Package projects ports the project CRUD logic from
// api/app/routers/projects.py to Go. It operates directly on *sql.DB and
// exposes typed functions consumed by the HTTP handlers.
package projects

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/chunksfts"
)

// ErrNotFound is returned when a project does not exist.
var ErrNotFound = errors.New("project not found")

// ErrConflict is returned when a project with the same path already exists.
var ErrConflict = errors.New("project already exists")

// ErrArtifactCleanup wraps failures from Delete's Artifacts hooks. The project
// row is already gone when this is returned — only off-database residue was
// left behind, which the admin Resources screen can reclaim later. Callers
// should log it rather than reporting the delete as failed.
var ErrArtifactCleanup = errors.New("project deleted but cleanup left residue")

// ErrOverlap is returned when the new project path is nested inside an
// existing project (or vice versa). Overlapping projects double-index the
// same files, blow up storage, and make search results ambiguous —
// always indicates a registration mistake the operator should resolve.
var ErrOverlap = errors.New("project path overlaps an existing project")

// Settings mirrors Python ProjectSettings.
type Settings struct {
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize     int      `json:"max_file_size"`
}

// DefaultSettings returns default settings matching Python defaults.
func DefaultSettings() Settings {
	return Settings{
		ExcludePatterns: []string{
			"node_modules", ".git", ".venv", "__pycache__",
			"dist", "build", ".next", ".cache", ".DS_Store",
		},
		MaxFileSize: 524288,
	}
}

// Stats mirrors Python ProjectStats.
type Stats struct {
	TotalFiles   int `json:"total_files"`
	IndexedFiles int `json:"indexed_files"`
	TotalChunks  int `json:"total_chunks"`
	TotalSymbols int `json:"total_symbols"`
}

// Project is the full project record returned from the database.
type Project struct {
	HostPath string
	// PathHash is the STORED path_hash column — the canonical URL identity
	// the dashboard links to and GetByHash resolves against. It is returned
	// verbatim rather than recomputed from HostPath: a project's host_path
	// and its stored path_hash can legitimately diverge (e.g. a local
	// project whose host_path is the bare filesystem path while path_hash
	// is keyed as sha1("local:{machine}:{path}")), and recomputing from
	// host_path would yield a hash that no lookup matches → 404.
	PathHash      string
	ContainerPath string
	Languages     []string
	Settings      Settings
	Stats         Stats
	Status        string
	CreatedAt     string
	UpdatedAt     string
	LastIndexedAt *string
	// IndexedWithModel is the embedding model identifier captured at the
	// last successful FinishIndexing. nil for projects that have never been
	// indexed under PR-E (or never indexed at all). The dashboard renders
	// nil as a neutral "Unknown" badge, NOT as drift.
	IndexedWithModel *string
	// OwnerUserID is the user who owns this personal (locally indexed)
	// project. nil = ownerless, the canonical state for EXTERNAL projects
	// (those with a git_repos row). See the auth model in db/schema.go.
	OwnerUserID *string
	// DisplayPath is the human-readable path (the real filesystem path for
	// local projects, the github path for external). HostPath is the identity
	// key and may be namespaced; clients should display DisplayPath.
	DisplayPath string
	// MachineID / MachineLabel identify the machine a local project was
	// indexed on. nil for external (and legacy) projects.
	MachineID    *string
	MachineLabel *string
	// FullSyncRequired flags that this project's index is format-stale and
	// needs a complete rebuild (e.g. chunker/embedding format changed under
	// it). Informational only — it drives the dashboard "out of sync" badge;
	// an admin triggers the resync, which clears the flag on success.
	FullSyncRequired bool
	// FullSyncReason is the human-readable explanation shown with the badge.
	// nil when FullSyncRequired is false.
	FullSyncReason *string
}

// CreateRequest mirrors Python ProjectCreate.
type CreateRequest struct {
	// HostPath is the REAL filesystem path the caller is registering (for
	// external repos, the github path). It becomes display_path; the stored
	// identity key is derived from it (namespaced with MachineID for locals).
	HostPath string
	// OwnerUserID, when non-empty, is stored as the project's owner (personal
	// project). Empty → NULL (ownerless), used by the external-repo path.
	OwnerUserID string
	// MachineID, when non-empty, marks this as a LOCAL project and namespaces
	// the identity key as local:{MachineID}:{HostPath} so the same path on
	// different machines/users does not collide. Empty → external (no
	// namespacing). MachineLabel is os.Hostname() for display only.
	MachineID    string
	MachineLabel string
}

// LocalProjectKey returns the namespaced identity key for a local project.
// MUST stay byte-identical to the CLI's key computation (cli client) so the
// path_hash both sides derive matches. Format: "local:{machineID}:{path}".
func LocalProjectKey(machineID, path string) string {
	return "local:" + machineID + ":" + path
}

// UpdateRequest mirrors Python ProjectUpdate.
type UpdateRequest struct {
	Settings *Settings
}

// HashPath returns the first 16 hex chars of SHA1(path), matching
// Python's hash_project_path in api/app/core/path_encoding.py.
// Used to encode project paths in URL segments.
func HashPath(path string) string {
	return hashPath(path)
}

func hashPath(path string) string {
	h := sha1.New()
	h.Write([]byte(path))
	b := h.Sum(nil)
	const hexchars = "0123456789abcdef"
	out := make([]byte, 16)
	for i := range 8 {
		out[i*2] = hexchars[b[i]>>4]
		out[i*2+1] = hexchars[b[i]&0xf]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// Create inserts a new project. Returns ErrConflict if the path already
// exists, or ErrOverlap if the path is a parent or descendant of any existing
// project.
//
// We pass host_path through unchanged to match Python
// (api/app/routers/projects.py). Normalising here (e.g. stripping trailing
// slashes) risks 404s on subsequent GET/PATCH calls that carry the original
// path through their SHA1 hash.
func Create(ctx context.Context, db *sql.DB, req CreateRequest) (*Project, error) {
	displayPath := req.HostPath
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Identity key: namespaced per machine for local projects so the same
	// filesystem path on different machines/users does not collide; the real
	// path is kept as display_path. External projects (no MachineID) use the
	// path as-is — it is already globally unique (github.com/owner/repo@branch).
	key := displayPath
	if req.MachineID != "" {
		key = LocalProjectKey(req.MachineID, displayPath)
	}

	if conflict, err := findOverlap(ctx, db, key); err != nil {
		return nil, fmt.Errorf("check overlap: %w", err)
	} else if conflict != "" {
		return nil, fmt.Errorf("%w: %s already registered", ErrOverlap, conflict)
	}

	defaultSettings := DefaultSettings()
	settingsJSON, err := json.Marshal(defaultSettings)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	defaultStats := Stats{}
	statsJSON, err := json.Marshal(defaultStats)
	if err != nil {
		return nil, fmt.Errorf("marshal stats: %w", err)
	}

	var owner any
	if req.OwnerUserID != "" {
		owner = req.OwnerUserID
	}
	var machineID, machineLabel any
	if req.MachineID != "" {
		machineID = req.MachineID
	}
	if req.MachineLabel != "" {
		machineLabel = req.MachineLabel
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash, owner_user_id, display_path, machine_id, machine_label)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, displayPath, "[]", string(settingsJSON), string(statsJSON), "created", now, now, hashPath(key), owner, displayPath, machineID, machineLabel,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("%w: %s", ErrConflict, displayPath)
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return Get(ctx, db, key)
}

// findOverlap returns the host_path of the first existing project that is a
// parent or descendant of `candidate`, or "" if none. Same path is treated as
// "no overlap" — the unique-index on host_path raises ErrConflict for that
// case with a more specific message.
//
// Path comparison strips a single trailing slash from both sides and then
// requires either:
//   - existing is a prefix of candidate followed by '/' (existing is parent), or
//   - candidate is a prefix of existing followed by '/' (existing is descendant)
//
// Symlinks are intentionally NOT resolved: storing canonical paths would
// silently change identifiers across machines and break stored hashes.
func findOverlap(ctx context.Context, db *sql.DB, candidate string) (string, error) {
	cand := strings.TrimSuffix(candidate, "/")
	if cand == "" {
		return "", nil
	}

	rows, err := db.QueryContext(ctx, `SELECT host_path FROM projects`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return "", err
		}
		ex := strings.TrimSuffix(existing, "/")
		if ex == "" || ex == cand {
			continue
		}
		if strings.HasPrefix(cand, ex+"/") || strings.HasPrefix(ex, cand+"/") {
			return existing, nil
		}
	}
	return "", rows.Err()
}

// Get retrieves a project by its host_path. Returns ErrNotFound if absent.
func Get(ctx context.Context, db *sql.DB, hostPath string) (*Project, error) {
	row := db.QueryRowContext(ctx,
		`SELECT host_path, container_path, languages, settings, stats, status, created_at, updated_at, last_indexed_at, indexed_with_model, owner_user_id, display_path, machine_id, machine_label, path_hash, full_sync_required, full_sync_reason
		 FROM projects WHERE host_path = ?`, hostPath,
	)
	return scanProject(hostPath, row)
}

// GetByHash resolves a project by SHA1 hash prefix (matching Python
// resolve_project_path). Backed by the indexed `path_hash` column (m7 fix),
// so the lookup is O(log n) instead of a full-table scan + per-row hashing.
// For pre-m7 databases the hash column is backfilled on Open, so this query
// works uniformly across fresh and upgraded installs.
func GetByHash(ctx context.Context, db *sql.DB, pathHash string) (*Project, error) {
	var matched string
	err := db.QueryRowContext(ctx,
		`SELECT host_path FROM projects WHERE path_hash = ?`, pathHash,
	).Scan(&matched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: hash=%s", ErrNotFound, pathHash)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup by path_hash: %w", err)
	}
	return Get(ctx, db, matched)
}

// List returns all projects ordered by created_at descending.
func List(ctx context.Context, db *sql.DB) ([]Project, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT host_path, container_path, languages, settings, stats, status, created_at, updated_at, last_indexed_at, indexed_with_model, owner_user_id, display_path, machine_id, machine_label, path_hash, full_sync_required, full_sync_reason
		 FROM projects ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Patch updates mutable fields. Returns ErrNotFound if the project is absent.
func Patch(ctx context.Context, db *sql.DB, hostPath string, req UpdateRequest) (*Project, error) {
	if _, err := Get(ctx, db, hostPath); err != nil {
		return nil, err
	}

	if req.Settings == nil {
		// Nothing to update.
		return Get(ctx, db, hostPath)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	settingsJSON, err := json.Marshal(req.Settings)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	_, err = db.ExecContext(ctx,
		`UPDATE projects SET settings = ?, updated_at = ? WHERE host_path = ?`,
		string(settingsJSON), now, hostPath,
	)
	if err != nil {
		return nil, fmt.Errorf("update project: %w", err)
	}
	return Get(ctx, db, hostPath)
}

// SetStatus updates only the status (and updated_at) of a project. No-op if
// the host_path doesn't match a row — callers that need 404 semantics should
// check via Get first. Used by handlers and workers that need to flip a
// project into "indexing"/"indexed"/"error" without round-tripping a full
// Patch payload.
func SetStatus(ctx context.Context, db *sql.DB, hostPath, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx,
		`UPDATE projects SET status = ?, updated_at = ? WHERE host_path = ?`,
		status, now, hostPath)
	return err
}

// Artifacts is the off-database residue a delete has to clean up, supplied by
// the caller because this package must not reach into the vector store or the
// filesystem.
//
// It exists because FK CASCADE only reaches rows. A project also owns a
// vector collection and a cloned checkout. Before this hook, deleting a
// project left both; a server that had been used for a while accumulated
// hundreds of thousands of orphaned vector documents that nothing could reach
// and nothing would ever free.
//
// Nil members are skipped, so a caller wires only what it has.
type Artifacts struct {
	// DropCollection removes the project's vector collection.
	DropCollection func(hostPath string) error
	// RemoveCloneDir removes the project's on-disk checkout, if any.
	RemoveCloneDir func(hostPath string) error
}

// Delete removes a project and its cascading records, then its artifacts.
// Returns ErrNotFound if absent. art may be nil.
//
// chunks_meta and chunks_fts are not bound to projects via FK because
// chunks_fts is a virtual table and cannot participate in foreign keys.
// The FTS wipe runs FIRST, in bounded batches (its own short transactions),
// because a big project's trigram-FTS delete can take minutes and SQLite has a
// single writer — one monolithic tx here starved every concurrent writer (see
// chunksfts.DeleteByProject). Failure midway leaves the projects row intact, so
// a retried Delete resumes the wipe; FTS rows never outlive the project row.
//
// Artifacts run LAST, after the row is gone, and their errors are joined and
// returned without undoing the delete. That ordering is deliberate: a failed
// os.RemoveAll then leaves a reclaimable orphan that the admin Resources
// screen will find and offer to clean, whereas failing the whole delete would
// leave a project the operator asked to remove and cannot.
func Delete(ctx context.Context, db *sql.DB, hostPath string, art *Artifacts) error {
	if _, err := Get(ctx, db, hostPath); err != nil {
		return err
	}
	if err := chunksfts.DeleteByProject(ctx, db, hostPath); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM projects WHERE host_path = ?`, hostPath); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if art == nil {
		return nil
	}
	var errs []error
	if art.DropCollection != nil {
		if err := art.DropCollection(hostPath); err != nil {
			errs = append(errs, fmt.Errorf("drop vector collection: %w", err))
		}
	}
	if art.RemoveCloneDir != nil {
		if err := art.RemoveCloneDir(hostPath); err != nil {
			errs = append(errs, fmt.Errorf("remove clone directory: %w", err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	// Wrapped in a sentinel so callers can tell "the project is still there"
	// from "the project is gone but left residue" — the two need opposite
	// HTTP responses.
	return fmt.Errorf("%w: %w", ErrArtifactCleanup, errors.Join(errs...))
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func scanProject(hostPath string, row *sql.Row) (*Project, error) {
	var (
		hp, containerPath       string
		langsJSON, settingsJSON string
		statsJSON, status       string
		createdAt, updatedAt    string
		lastIndexedAt           *string
		indexedWithModel        *string
		ownerUserID             *string
		displayPath             *string
		machineID               *string
		machineLabel            *string
		pathHash                *string
		fullSyncRequired        bool
		fullSyncReason          *string
	)
	err := row.Scan(
		&hp, &containerPath,
		&langsJSON, &settingsJSON, &statsJSON,
		&status, &createdAt, &updatedAt, &lastIndexedAt, &indexedWithModel, &ownerUserID,
		&displayPath, &machineID, &machineLabel, &pathHash, &fullSyncRequired, &fullSyncReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hostPath)
	}
	if err != nil {
		return nil, fmt.Errorf("scan project row: %w", err)
	}
	return buildProject(hp, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt, lastIndexedAt, indexedWithModel, ownerUserID, displayPath, machineID, machineLabel, pathHash, fullSyncRequired, fullSyncReason)
}

func scanProjectRow(rows *sql.Rows) (*Project, error) {
	var (
		hostPath, containerPath string
		langsJSON, settingsJSON string
		statsJSON, status       string
		createdAt, updatedAt    string
		lastIndexedAt           *string
		indexedWithModel        *string
		ownerUserID             *string
		displayPath             *string
		machineID               *string
		machineLabel            *string
		pathHash                *string
		fullSyncRequired        bool
		fullSyncReason          *string
	)
	if err := rows.Scan(
		&hostPath, &containerPath,
		&langsJSON, &settingsJSON, &statsJSON,
		&status, &createdAt, &updatedAt, &lastIndexedAt, &indexedWithModel, &ownerUserID,
		&displayPath, &machineID, &machineLabel, &pathHash, &fullSyncRequired, &fullSyncReason,
	); err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	return buildProject(hostPath, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt, lastIndexedAt, indexedWithModel, ownerUserID, displayPath, machineID, machineLabel, pathHash, fullSyncRequired, fullSyncReason)
}

func buildProject(hostPath, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt string, lastIndexedAt, indexedWithModel, ownerUserID, displayPath, machineID, machineLabel, pathHash *string, fullSyncRequired bool, fullSyncReason *string) (*Project, error) {
	var langs []string
	if err := json.Unmarshal([]byte(langsJSON), &langs); err != nil {
		langs = nil
	}

	var settings Settings
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
		settings = DefaultSettings()
	}

	var stats Stats
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		stats = Stats{}
	}

	dp := hostPath
	if displayPath != nil && *displayPath != "" {
		dp = *displayPath
	}
	// Fall back to the host-path hash only when the stored column is
	// absent (pre-m7 rows backfill it on Open, so this is belt-and-braces).
	ph := ""
	if pathHash != nil && *pathHash != "" {
		ph = *pathHash
	} else {
		ph = hashPath(hostPath)
	}
	return &Project{
		HostPath:         hostPath,
		PathHash:         ph,
		ContainerPath:    containerPath,
		Languages:        langs,
		Settings:         settings,
		Stats:            stats,
		Status:           status,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		LastIndexedAt:    lastIndexedAt,
		IndexedWithModel: indexedWithModel,
		OwnerUserID:      ownerUserID,
		DisplayPath:      dp,
		MachineID:        machineID,
		MachineLabel:     machineLabel,
		FullSyncRequired: fullSyncRequired,
		FullSyncReason:   fullSyncReason,
	}, nil
}

// SetOwner reassigns (or clears, when ownerUserID == "") a project's owner.
// Used by the admin reassign-owner endpoint. Returns ErrNotFound if absent.
func SetOwner(ctx context.Context, db *sql.DB, hostPath, ownerUserID string) error {
	var owner any
	if ownerUserID != "" {
		owner = ownerUserID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx,
		`UPDATE projects SET owner_user_id = ?, updated_at = ? WHERE host_path = ?`,
		owner, now, hostPath)
	if err != nil {
		return fmt.Errorf("set project owner: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
