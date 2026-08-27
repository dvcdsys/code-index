package searchstats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Where a resolved setting came from. Surfaced to the dashboard so an admin can
// tell a decision from a default — the same distinction the runtime-settings
// screen makes, and for the same reason: "off" means something different when
// somebody chose it than when nobody has.
const (
	SourceDatabase    = "database"
	SourceEnvironment = "environment"
	SourceDefault     = "default"
)

// Settings is the resolved state of the feature.
type Settings struct {
	Enabled   bool   `json:"enabled"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// SettingsStore reads and writes the admin's decision. It lives in the SYSTEM
// database, not in searchstats.db — the setting has to be readable when the
// statistics database is closed, which is precisely the state it describes.
type SettingsStore struct {
	db *sql.DB
	// envEnabled is CIX_SEARCH_STATS_ENABLED, consulted only when no row has
	// been saved.
	envEnabled bool
	// envSet distinguishes "the operator set the variable to false" from "the
	// operator set nothing". Both resolve to off, but only one of them is a
	// decision, and the dashboard says which.
	envSet bool
}

// NewSettingsStore wires the resolver. envEnabled/envSet come from config.
func NewSettingsStore(db *sql.DB, envEnabled, envSet bool) *SettingsStore {
	return &SettingsStore{db: db, envEnabled: envEnabled, envSet: envSet}
}

// Get resolves the setting: a saved decision wins, then the environment, then
// off.
//
// The order is what makes a redeploy safe. An admin who turns the feature on in
// the dashboard must not have it turned off again by the next container start
// carrying the old environment — so the database outranks the environment, and
// the environment exists to give a fleet its starting position rather than to
// re-assert itself forever.
func (s *SettingsStore) Get(ctx context.Context) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{Enabled: false, Source: SourceDefault}, nil
	}
	var enabled int
	var updatedAt string
	var updatedBy sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, updated_at, updated_by FROM search_stats_config WHERE id = 1`).
		Scan(&enabled, &updatedAt, &updatedBy)
	switch {
	case err == nil:
		return Settings{
			Enabled:   enabled != 0,
			Source:    SourceDatabase,
			UpdatedAt: updatedAt,
			UpdatedBy: updatedBy.String,
		}, nil
	case errors.Is(err, sql.ErrNoRows):
		if s.envSet {
			return Settings{Enabled: s.envEnabled, Source: SourceEnvironment}, nil
		}
		return Settings{Enabled: false, Source: SourceDefault}, nil
	default:
		return Settings{}, fmt.Errorf("searchstats: read settings: %w", err)
	}
}

// Set records the admin's decision. From this point the environment no longer
// speaks for this server.
func (s *SettingsStore) Set(ctx context.Context, enabled bool, by string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("searchstats: no settings store")
	}
	flag := 0
	if enabled {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO search_stats_config (id, enabled, updated_at, updated_by)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled    = excluded.enabled,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		flag, time.Now().UTC().Format(time.RFC3339Nano), nullIfBlank(by))
	if err != nil {
		return fmt.Errorf("searchstats: save settings: %w", err)
	}
	return nil
}

func nullIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}
