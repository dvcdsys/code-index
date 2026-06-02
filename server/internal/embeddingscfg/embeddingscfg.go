// Package embeddingscfg persists the pluggable-provider selection +
// per-provider config blob in runtime_settings. It sits alongside
// runtimecfg (which still owns the ollama-tuning subset for backward
// compat) but exposes a separate Service so the admin endpoints have
// a single clean surface for provider switching.
//
// Resolution flow at boot:
//
//	row.embedding_provider IS NULL  → seed from CIX_EMBEDDING_PROVIDER
//	                                  (default "ollama") + env-derived
//	                                  ollama config blob, write to DB.
//	row.embedding_provider IS NOT NULL → load from DB verbatim. Env
//	                                     vars (other than API-key envs
//	                                     read live by providers) are
//	                                     ignored — DB is authoritative.
package embeddingscfg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Snapshot is what's currently persisted. Returned by Get; supplied
// to Service.Save for atomic updates.
type Snapshot struct {
	Kind   string          // "ollama" | "openai" | "voyage"
	Config json.RawMessage // provider-specific blob; opaque to this package
}

// Service is the persistence-only layer. Stateless aside from the
// *sql.DB it wraps.
type Service struct {
	db *sql.DB
}

// New constructs a Service over the given *sql.DB.
func New(db *sql.DB) *Service { return &Service{db: db} }

// Get returns the persisted provider selection. (Snapshot{}, false, nil)
// means the runtime_settings row has no provider yet — the caller
// should seed it from env.
func (s *Service) Get(ctx context.Context) (Snapshot, bool, error) {
	var (
		kind sql.NullString
		cfg  sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT embedding_provider, embedding_provider_config
		FROM runtime_settings WHERE id = 1
	`).Scan(&kind, &cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("select embedding_provider: %w", err)
	}
	if !kind.Valid || kind.String == "" {
		return Snapshot{}, false, nil
	}
	snap := Snapshot{Kind: kind.String}
	if cfg.Valid && cfg.String != "" {
		snap.Config = json.RawMessage(cfg.String)
	}
	return snap, true, nil
}

// Save persists kind + config bytes. Inserts the row when absent
// (CHECK(id=1) keeps it single-row). updated_by labels the actor for
// audit; pass "" when the change is server-internal (bootstrap seed).
func (s *Service) Save(ctx context.Context, snap Snapshot, updatedBy string) error {
	if snap.Kind == "" {
		return errors.New("embeddingscfg: kind is required")
	}
	if len(snap.Config) > 0 {
		if !json.Valid(snap.Config) {
			return errors.New("embeddingscfg: config is not valid JSON")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Try UPDATE first; if no rows affected the row doesn't exist yet
	// and we INSERT with the required CHECK(id=1).
	res, err := s.db.ExecContext(ctx, `
		UPDATE runtime_settings
		SET embedding_provider = ?, embedding_provider_config = ?,
		    updated_at = ?, updated_by = ?
		WHERE id = 1
	`, snap.Kind, string(snap.Config), now, nullableUpdater(updatedBy))
	if err != nil {
		return fmt.Errorf("update embedding_provider: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO runtime_settings (
			id, embedding_provider, embedding_provider_config,
			updated_at, updated_by
		) VALUES (1, ?, ?, ?, ?)
	`, snap.Kind, string(snap.Config), now, nullableUpdater(updatedBy))
	if err != nil {
		return fmt.Errorf("insert embedding_provider: %w", err)
	}
	return nil
}

func nullableUpdater(v string) any {
	if v == "" {
		return nil
	}
	return v
}
