// Package tunnelcfg persists the single dashboard-managed Managed Tunnel
// configuration row (tunnel_config). The Managed Tunnels feature has no
// env enable flag — enable/mode/hostname/token are stored here and edited
// from the dashboard. The Cloudflare named-tunnel connector token is a
// secret, so it is encrypted at rest via the same secrets.Service used for
// GitHub PATs.
package tunnelcfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/secrets"
)

const (
	ProviderCloudflare = "cloudflare"
	ProviderNgrok      = "ngrok"
	ModeNamed          = "named"
	ModeQuick          = "quick"
)

// ErrSecretsUnavailable is returned when a token write is attempted without
// a configured secrets service (encryption key boot failure).
var ErrSecretsUnavailable = errors.New("secrets service not configured")

// Settings is the dashboard-facing view: never carries the plaintext token,
// only whether one is stored.
type Settings struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode"`
	Hostname  string `json:"hostname"`
	TokenSet  bool   `json:"token_set"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// Resolved is the internal view used by the tunnel manager: includes the
// decrypted token.
type Resolved struct {
	Enabled  bool
	Provider string
	Mode     string
	Hostname string
	Token    string
}

// Patch is a partial update. A nil field is left unchanged. Token semantics:
// nil = keep existing, pointer to "" = clear, pointer to value = set.
type Patch struct {
	Enabled  *bool
	Provider *string
	Mode     *string
	Hostname *string
	Token    *string
}

type Service struct {
	DB      *sql.DB
	Secrets *secrets.Service
}

func New(db *sql.DB, sec *secrets.Service) *Service {
	return &Service{DB: db, Secrets: sec}
}

func defaults() Settings {
	return Settings{Enabled: false, Provider: ProviderCloudflare, Mode: ModeQuick}
}

type row struct {
	enabled        bool
	provider       string
	mode           string
	hostname       string
	encryptedToken []byte
	updatedAt      string
	updatedBy      sql.NullString
}

func (s *Service) load(ctx context.Context) (row, bool, error) {
	var r row
	err := s.DB.QueryRowContext(ctx,
		`SELECT enabled, provider, mode, hostname, encrypted_token, updated_at, updated_by
		 FROM tunnel_config WHERE id = 1`).
		Scan(&r.enabled, &r.provider, &r.mode, &r.hostname, &r.encryptedToken, &r.updatedAt, &r.updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("load tunnel_config: %w", err)
	}
	return r, true, nil
}

// Get returns the dashboard-facing settings (no plaintext token).
func (s *Service) Get(ctx context.Context) (Settings, error) {
	r, ok, err := s.load(ctx)
	if err != nil {
		return Settings{}, err
	}
	if !ok {
		return defaults(), nil
	}
	return Settings{
		Enabled:   r.enabled,
		Provider:  r.provider,
		Mode:      r.mode,
		Hostname:  r.hostname,
		TokenSet:  len(r.encryptedToken) > 0,
		UpdatedAt: r.updatedAt,
		UpdatedBy: r.updatedBy.String,
	}, nil
}

// Resolve returns the settings with the decrypted token, for the manager.
func (s *Service) Resolve(ctx context.Context) (Resolved, error) {
	r, ok, err := s.load(ctx)
	if err != nil {
		return Resolved{}, err
	}
	if !ok {
		d := defaults()
		return Resolved{Enabled: d.Enabled, Provider: d.Provider, Mode: d.Mode}, nil
	}
	res := Resolved{
		Enabled:  r.enabled,
		Provider: r.provider,
		Mode:     r.mode,
		Hostname: r.hostname,
	}
	if len(r.encryptedToken) > 0 {
		if s.Secrets == nil {
			return Resolved{}, ErrSecretsUnavailable
		}
		plain, derr := s.Secrets.Decrypt(r.encryptedToken)
		if derr != nil {
			return Resolved{}, fmt.Errorf("decrypt tunnel token: %w", derr)
		}
		res.Token = string(plain)
	}
	return res, nil
}

// Set applies a patch and returns the resulting dashboard-facing settings.
// Validates the merged config: a named tunnel that is enabled requires a
// hostname and a token (existing or newly supplied).
func (s *Service) Set(ctx context.Context, patch Patch, updatedBy string) (Settings, error) {
	r, ok, err := s.load(ctx)
	if err != nil {
		return Settings{}, err
	}
	if !ok {
		d := defaults()
		r = row{enabled: d.Enabled, provider: d.Provider, mode: d.Mode}
	}

	if patch.Enabled != nil {
		r.enabled = *patch.Enabled
	}
	if patch.Provider != nil {
		pv := strings.ToLower(strings.TrimSpace(*patch.Provider))
		if pv != ProviderCloudflare && pv != ProviderNgrok {
			return Settings{}, fmt.Errorf("provider must be %q or %q", ProviderCloudflare, ProviderNgrok)
		}
		r.provider = pv
	}
	if patch.Mode != nil {
		m := strings.ToLower(strings.TrimSpace(*patch.Mode))
		if m != ModeNamed && m != ModeQuick {
			return Settings{}, fmt.Errorf("mode must be %q or %q", ModeNamed, ModeQuick)
		}
		r.mode = m
	}
	if patch.Hostname != nil {
		r.hostname = strings.TrimSpace(*patch.Hostname)
	}
	if patch.Token != nil {
		tok := strings.TrimSpace(*patch.Token)
		if tok == "" {
			r.encryptedToken = nil
		} else {
			if s.Secrets == nil {
				return Settings{}, ErrSecretsUnavailable
			}
			enc, eerr := s.Secrets.Encrypt([]byte(tok))
			if eerr != nil {
				return Settings{}, fmt.Errorf("encrypt tunnel token: %w", eerr)
			}
			r.encryptedToken = enc
		}
	}
	if r.provider == "" {
		r.provider = ProviderCloudflare
	}

	// Validate the merged config when enabling. Requirements differ per
	// provider: cloudflare quick needs nothing; cloudflare named needs a
	// hostname + connector token; ngrok always needs an authtoken (even
	// ephemeral) and additionally a reserved domain in named mode.
	if r.enabled {
		named := r.mode == ModeNamed
		if named && r.hostname == "" {
			return Settings{}, fmt.Errorf("named mode requires a hostname")
		}
		switch r.provider {
		case ProviderCloudflare:
			if named && len(r.encryptedToken) == 0 {
				return Settings{}, fmt.Errorf("cloudflare named mode requires a connector token")
			}
		case ProviderNgrok:
			if len(r.encryptedToken) == 0 {
				return Settings{}, fmt.Errorf("ngrok requires an authtoken")
			}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO tunnel_config (id, enabled, provider, mode, hostname, encrypted_token, updated_at, updated_by)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   enabled=excluded.enabled,
		   provider=excluded.provider,
		   mode=excluded.mode,
		   hostname=excluded.hostname,
		   encrypted_token=excluded.encrypted_token,
		   updated_at=excluded.updated_at,
		   updated_by=excluded.updated_by`,
		r.enabled, r.provider, r.mode, r.hostname, nullableBlob(r.encryptedToken), now, nullableStr(updatedBy),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("upsert tunnel_config: %w", err)
	}
	return s.Get(ctx)
}

func nullableBlob(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
