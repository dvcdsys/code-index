package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/dbmaint"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// runResetPassword implements `cix-server -reset-password <email>`: an
// offline recovery path for an operator locked out of the dashboard (the
// admin-initiated reset endpoint requires an authenticated admin, which is
// exactly what a locked-out operator no longer has). It opens the SQLite
// database directly — no HTTP, no auth — so possession of the DB file is the
// authorization boundary, same as any other local recovery tool.
//
// The new password comes from stdin when piped (first line, so wrapper
// scripts can `read -s` and pipe it in); otherwise a random one is generated
// and printed. Either way the account is flagged must_change_password=1 and
// all of its sessions are revoked, mirroring the dashboard's admin reset.
func runResetPassword(email string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Settle any interrupted compaction first. Without this, a database
	// caught mid-swap is either missing (the stat below would report "not
	// found" at a path that is perfectly correct) or is the pre-compaction
	// original that the next server start would replace — so the reset would
	// land in a file about to be discarded.
	if err := dbmaint.Reconcile(context.Background(), cfg.SQLitePath, slog.Default()); err != nil {
		return fmt.Errorf("reconcile database maintenance state: %w", err)
	}

	// Opening a missing path would CREATE an empty DB and "successfully"
	// find no users in it — worse than useless for someone trying to
	// recover access. Refuse instead and point at the knob.
	if _, err := os.Stat(cfg.SQLitePath); err != nil {
		return fmt.Errorf("database not found at %s — set CIX_SQLITE_PATH to your server's database file", cfg.SQLitePath)
	}

	database, err := db.Open(cfg.SQLitePath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	return resetPassword(context.Background(), database, email, os.Stdin, os.Stdout)
}

// resetPassword is the testable core: split from runResetPassword so tests
// can drive it with an in-memory DB and fake stdin/stdout.
func resetPassword(ctx context.Context, database *sql.DB, email string, stdin io.Reader, stdout io.Writer) error {
	usrSvc := users.New(database)
	sessSvc := sessions.New(database)

	u, err := usrSvc.GetByEmail(ctx, email)
	if errors.Is(err, users.ErrNotFound) {
		// Whoever runs this already has the DB file; listing emails leaks
		// nothing they couldn't `sqlite3` for, and it catches typos.
		all, lerr := usrSvc.List(ctx)
		if lerr != nil {
			return fmt.Errorf("no user with email %q (and could not list users: %v)", email, lerr)
		}
		var emails []string
		for _, x := range all {
			emails = append(emails, x.Email)
		}
		return fmt.Errorf("no user with email %q — existing users: %s", email, strings.Join(emails, ", "))
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}

	password, generated, err := passwordFromStdinOrGenerate(stdin)
	if err != nil {
		return err
	}

	if err := usrSvc.AdminResetPassword(ctx, u.ID, password); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if err := sessSvc.DeleteAllForUser(ctx, u.ID); err != nil {
		return fmt.Errorf("password was reset, but revoking existing sessions failed: %w", err)
	}

	fmt.Fprintf(stdout, "Password reset for %s (%s).\n", u.Email, u.Role)
	if generated {
		fmt.Fprintf(stdout, "Temporary password: %s\n", password)
	}
	fmt.Fprintln(stdout, "All sessions for this account were revoked; the next login forces a password change.")
	if u.DisabledAt != nil {
		fmt.Fprintln(stdout, "WARNING: this account is DISABLED — it still cannot log in. Re-enable it from another admin account (Dashboard → Users).")
	}
	return nil
}

// passwordFromStdinOrGenerate returns the first line of stdin when input is
// piped in, or a freshly generated random password otherwise. generated
// reports which path was taken so the caller knows whether to print it.
func passwordFromStdinOrGenerate(stdin io.Reader) (password string, generated bool, err error) {
	if f, ok := stdin.(*os.File); ok {
		if st, serr := f.Stat(); serr == nil && st.Mode()&os.ModeCharDevice != 0 {
			// Interactive terminal — nothing piped; generate.
			pw, gerr := generatePassword()
			return pw, true, gerr
		}
	}
	line, rerr := bufio.NewReader(stdin).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return "", false, fmt.Errorf("read password from stdin: %w", rerr)
	}
	if line == "" {
		pw, gerr := generatePassword()
		return pw, true, gerr
	}
	if len(line) < users.MinPasswordLength {
		return "", false, fmt.Errorf("password from stdin must be at least %d characters", users.MinPasswordLength)
	}
	return line, false, nil
}

// generatePassword returns 20 chars from an alphabet with no shell-special
// or easily confused characters (0/O, 1/l/I), so the printed value survives
// copy-paste into any terminal or password field.
func generatePassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const length = 20
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	out := make([]byte, length)
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
