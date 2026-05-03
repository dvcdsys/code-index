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
)

// ErrNotFound is returned when a project does not exist.
var ErrNotFound = errors.New("project not found")

// ErrConflict is returned when a project with the same path already exists.
var ErrConflict = errors.New("project already exists")

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
	HostPath      string
	ContainerPath string
	Languages     []string
	Settings      Settings
	Stats         Stats
	Status        string
	CreatedAt     string
	UpdatedAt     string
	LastIndexedAt *string
}

// CreateRequest mirrors Python ProjectCreate.
type CreateRequest struct {
	HostPath string
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
	hostPath := req.HostPath
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if conflict, err := findOverlap(ctx, db, hostPath); err != nil {
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

	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hostPath, hostPath, "[]", string(settingsJSON), string(statsJSON), "created", now, now, hashPath(hostPath),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("%w: %s", ErrConflict, hostPath)
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return Get(ctx, db, hostPath)
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
		`SELECT host_path, container_path, languages, settings, stats, status, created_at, updated_at, last_indexed_at
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
		`SELECT host_path, container_path, languages, settings, stats, status, created_at, updated_at, last_indexed_at
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

// Delete removes a project and its cascading records. Returns ErrNotFound if absent.
func Delete(ctx context.Context, db *sql.DB, hostPath string) error {
	if _, err := Get(ctx, db, hostPath); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `DELETE FROM projects WHERE host_path = ?`, hostPath)
	return err
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func scanProject(hostPath string, row *sql.Row) (*Project, error) {
	var (
		hp, containerPath         string
		langsJSON, settingsJSON   string
		statsJSON, status         string
		createdAt, updatedAt      string
		lastIndexedAt             *string
	)
	err := row.Scan(
		&hp, &containerPath,
		&langsJSON, &settingsJSON, &statsJSON,
		&status, &createdAt, &updatedAt, &lastIndexedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hostPath)
	}
	if err != nil {
		return nil, fmt.Errorf("scan project row: %w", err)
	}
	return buildProject(hp, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt, lastIndexedAt)
}

func scanProjectRow(rows *sql.Rows) (*Project, error) {
	var (
		hostPath, containerPath string
		langsJSON, settingsJSON string
		statsJSON, status       string
		createdAt, updatedAt   string
		lastIndexedAt           *string
	)
	if err := rows.Scan(
		&hostPath, &containerPath,
		&langsJSON, &settingsJSON, &statsJSON,
		&status, &createdAt, &updatedAt, &lastIndexedAt,
	); err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	return buildProject(hostPath, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt, lastIndexedAt)
}

func buildProject(hostPath, containerPath, langsJSON, settingsJSON, statsJSON, status, createdAt, updatedAt string, lastIndexedAt *string) (*Project, error) {
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

	return &Project{
		HostPath:      hostPath,
		ContainerPath: containerPath,
		Languages:     langs,
		Settings:      settings,
		Stats:         stats,
		Status:        status,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		LastIndexedAt: lastIndexedAt,
	}, nil
}
