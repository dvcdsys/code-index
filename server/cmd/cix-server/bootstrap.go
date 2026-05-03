package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// bootstrapAuth seeds the dashboard auth model on a cold start.
//
// Decision matrix (when CIX_AUTH_DISABLED is FALSE):
//
//	users.Count == 0:
//	  EMAIL + PASSWORD both set      → create admin (must_change_password=1)
//	  exactly one of the two set     → fatal: tell the operator which is missing
//	  neither set                    → fatal: refuse to start with open auth
//	users.Count > 0:
//	  EMAIL or PASSWORD set          → log warning, ignore env (DB wins)
//	  neither set                    → no-op
//
// CIX_API_KEY is imported as a one-time legacy api_key on a fresh DB so
// existing CLI clients keep working through the upgrade. After the
// import the env var becomes a no-op (the dashboard takes over key
// management).
func bootstrapAuth(ctx context.Context, cfg *config.Config, logger *slog.Logger, usr *users.Service, ak *apikeys.Service) error {
	count, err := usr.Count(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}

	hasEmail := cfg.BootstrapAdminEmail != ""
	hasPassword := cfg.BootstrapAdminPassword != ""

	// Catch the half-configured case BEFORE the populated-DB branch — a
	// stale env var on an already-bootstrapped deployment is still worth
	// logging, but starting up with only one of the two on a fresh DB is
	// a real misconfiguration we should refuse loud.
	if count == 0 && hasEmail != hasPassword {
		missing := "CIX_BOOTSTRAP_ADMIN_PASSWORD"
		if !hasEmail {
			missing = "CIX_BOOTSTRAP_ADMIN_EMAIL"
		}
		return fmt.Errorf("incomplete bootstrap configuration: %s is set but %s is empty.\n"+
			"  Both env vars are required together to seed the first admin. Example:\n"+
			"      CIX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \\\n"+
			"      CIX_BOOTSTRAP_ADMIN_PASSWORD='change-me-on-first-login' \\\n"+
			"      ./cix-server", oppositeOf(missing), missing)
	}

	switch {
	case count == 0 && hasEmail && hasPassword:
		u, err := usr.Create(ctx, cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword, users.RoleAdmin, true)
		if err != nil {
			if errors.Is(err, users.ErrEmailTaken) {
				return fmt.Errorf("bootstrap admin email already taken in fresh DB — refusing to continue")
			}
			return fmt.Errorf("create bootstrap admin: %w", err)
		}
		logger.Warn("bootstrap admin created from CIX_BOOTSTRAP_ADMIN_EMAIL + CIX_BOOTSTRAP_ADMIN_PASSWORD; user will be forced to change password on first login",
			"email", u.Email, "user_id", u.ID)

		if cfg.APIKey != "" {
			if _, err := ak.ImportLegacy(ctx, u.ID, "env-bootstrap", cfg.APIKey); err != nil {
				logger.Error("could not import CIX_API_KEY as legacy api_key — CLI clients using that key will fail until you create one via the dashboard", "err", err)
			} else {
				logger.Warn("CIX_API_KEY imported as 'env-bootstrap' api_key for the bootstrap admin — rotate it via the dashboard at your earliest convenience")
			}
		}

	case count == 0:
		return fmt.Errorf("no users in database and the bootstrap admin env vars are not set: refuse to start.\n" +
			"  Set BOTH CIX_BOOTSTRAP_ADMIN_EMAIL and CIX_BOOTSTRAP_ADMIN_PASSWORD to seed the first admin\n" +
			"  (you will be forced to change the password on first login). Example:\n" +
			"      CIX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \\\n" +
			"      CIX_BOOTSTRAP_ADMIN_PASSWORD='change-me-on-first-login' \\\n" +
			"      ./cix-server\n" +
			"  Or set CIX_AUTH_DISABLED=true for local dev (every endpoint becomes public)")

	default:
		if hasEmail || hasPassword {
			logger.Info("CIX_BOOTSTRAP_ADMIN_* ignored — database already has users (DB wins over env)")
		}
	}

	return nil
}

// oppositeOf returns the env-var name that goes with the supplied missing one.
// Tiny helper to keep the half-configured error message readable.
func oppositeOf(missing string) string {
	if missing == "CIX_BOOTSTRAP_ADMIN_EMAIL" {
		return "CIX_BOOTSTRAP_ADMIN_PASSWORD"
	}
	return "CIX_BOOTSTRAP_ADMIN_EMAIL"
}
