package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/client"
	"github.com/dvcdsys/code-index/cli/internal/config"
)

// First-run setup.
//
// This exists because of a hard server-side constraint, not for polish:
// cix-server refuses to start against an empty database unless both
// CIX_BOOTSTRAP_ADMIN_EMAIL and CIX_BOOTSTRAP_ADMIN_PASSWORD are set
// (server/cmd/cix-server/bootstrap.go, `case count == 0`). It will not invent
// an admin account silently. A drag-installed app with no configuration can
// therefore never start its own server, so the wizard is load-bearing.

const bootstrapServerName = "local"

// needsFirstRun reports whether this installation still has to be set up.
//
// Two conditions, and the second one matters more than it looks: no
// ~/.cix/server.env, or no database at the path that file names.
//
// Keying on the config file alone was wrong. Delete ~/.cix/data and the app
// carried on as a configured install — the server then recreated the directory
// itself and silently minted a fresh admin from the bootstrap credentials still
// sitting in server.env, with the original generated password rather than
// whichever one the user had since set. A missing database is not a
// configuration this app should quietly repair; it is an installation that
// needs setting up again, and saying so is the whole point.
//
// Not covered here: a database file that exists but holds no users. That case
// is not rare at all — it is what a deleted data directory turns into on the
// very next start, because the server creates the file and runs migrations
// BEFORE it checks for an admin account, then refuses. Answering it from here
// would need the SQLite driver in the launcher; instead the refusal itself is
// recognised after the fact — see isBootstrapRefusal, and the Start handler
// that routes it back to setup.
func needsFirstRun() bool {
	path, err := serverEnvPath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return true
	}

	vars, err := readServerEnv()
	if err != nil {
		// Present but unreadable. Running the wizard would overwrite a file we
		// cannot even parse, so leave it alone and let the failure surface.
		logf("could not read %s: %v", path, err)
		return false
	}
	db := strings.TrimSpace(vars["CIX_SQLITE_PATH"])
	if db == "" {
		// Hand-edited beyond what we wrote. Not ours to second-guess.
		return false
	}
	if _, err := os.Stat(db); errors.Is(err, os.ErrNotExist) {
		logf("configured database %s is missing — treating this as an unconfigured install", db)
		return true
	}
	return false
}

// isBootstrapRefusal recognises the server's no-admin-account refusal in a log
// tail.
//
// This closes the gap needsFirstRun leaves open. Delete ~/.cix/data and the
// first start attempt — a login-time autostart as easily as a click — recreates
// an empty cix.db before bootstrapAuth refuses, so from then on the file exists
// and needsFirstRun answers false. The one thing that still knows the database
// has no accounts is the server itself, and it says so in words this matches:
// "incomplete bootstrap configuration" (an email left in server.env after the
// password was retired) and "no users in database" (neither var set). Both mean
// exactly one thing — there is no admin account and the server will not invent
// one — and for both, running setup again is the fix.
func isBootstrapRefusal(logTail string) bool {
	return strings.Contains(logTail, "incomplete bootstrap configuration") ||
		strings.Contains(logTail, "no users in database")
}

// retireBootstrapPassword drops CIX_BOOTSTRAP_ADMIN_PASSWORD from server.env
// once there is a running server, and therefore an account, that no longer
// needs it.
//
// The password seeds the very first admin and has no purpose afterwards, but it
// used to stay on disk forever — and stay authoritative. Wipe the database and
// the account came back with THAT password, not the one the user had set in the
// dashboard; a credential outliving the account it created, silently.
//
// The email stays: it is harmless, and the password-reset dialog offers it as
// the address to reset.
//
// Called only when the server is confirmed up. Bootstrap runs before the HTTP
// listener opens, so an answering /health is proof the account exists.
func retireBootstrapPassword() {
	vars, err := readServerEnv()
	if err != nil {
		return
	}
	if _, ok := vars["CIX_BOOTSTRAP_ADMIN_PASSWORD"]; !ok {
		return
	}
	delete(vars, "CIX_BOOTSTRAP_ADMIN_PASSWORD")
	if err := writeServerEnv(vars); err != nil {
		logf("could not remove the bootstrap password from server.env: %v", err)
		return
	}
	logf("removed CIX_BOOTSTRAP_ADMIN_PASSWORD from server.env — the admin account exists")
}

// runFirstRun walks the user through creating the admin account, writes
// server.env, and registers the server with the CLI.
//
// Returns errCancelled if the user backs out, which is not an error condition —
// the app stays running with Start disabled until they complete setup.
func runFirstRun(u *updater) error {
	// Short, and leading with the thing to type. The first version of this
	// opened with why an account is needed and buried the instruction in the
	// second paragraph — which reads like a sign-up form, and the one question
	// it left unanswered was the one everybody asks: where is my address going.
	// Nowhere. Saying so is worth more than the explanation it replaced.
	intro := "Enter an email address for the administrator account.\n\n" +
		"It is the login for the cix dashboard on this Mac — nothing is sent anywhere. " +
		"A password is generated for you, and setup then downloads the server (about 40 MB)."

	// On a re-run the previous admin's address is still in server.env, and the
	// most likely answer is the same one — so offer it, editable.
	priorEmail := ""
	if prior, err := readServerEnv(); err == nil {
		priorEmail = strings.TrimSpace(prior["CIX_BOOTSTRAP_ADMIN_EMAIL"])
	}

	email, err := prompt("Set up cix", intro, priorEmail)
	if err != nil {
		return err
	}
	email = strings.TrimSpace(email)
	for {
		if _, err := mail.ParseAddress(email); err == nil && email != "" {
			break
		}
		email, err = prompt("Set up cix", "That does not look like an email address. Try again:", email)
		if err != nil {
			return err
		}
		email = strings.TrimSpace(email)
	}

	// Before anything is written: the runtime is the one step here that can fail
	// for reasons outside this machine, and a setup that wrote server.env and a
	// launchd agent pointing at a server that was never downloaded would look
	// complete and be broken.
	if err := ensureRuntime(u, logProgress); err != nil {
		return fmt.Errorf("could not install the cix server: %w", err)
	}

	password, err := generatePassword()
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	apiKey, err := generateAPIKey()
	if err != nil {
		return fmt.Errorf("generate api key: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dataDir := filepath.Join(home, ".cix", "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	// Start from whatever is already configured. The wizard runs a second time
	// when the database has gone missing, and on that path server.env exists and
	// holds decisions the user made since — the port, and whether the server is
	// reachable from the network. Rebuilding the file from defaults would revert
	// them without saying so, which is a worse surprise than the one this whole
	// change exists to remove.
	vars, err := readServerEnv()
	if err != nil {
		vars = map[string]string{}
	}

	// Always fresh: the account and its key are being created now. On a repeat
	// run the previous key died with the database it was imported into.
	vars["CIX_BOOTSTRAP_ADMIN_EMAIL"] = email
	vars["CIX_BOOTSTRAP_ADMIN_PASSWORD"] = password
	// Imported as an "env-bootstrap" legacy key when the admin is created on a
	// fresh DB (bootstrap.go), so the CLI works from the first boot without
	// anyone visiting the dashboard to mint one.
	vars["CIX_API_KEY"] = apiKey

	// Defaults only where nothing has been chosen.
	setDefault(vars, "CIX_PORT", strconv.Itoa(defaultServerPort))
	setDefault(vars, "CIX_DATA_DIR", dataDir)
	setDefault(vars, "CIX_SQLITE_PATH", filepath.Join(dataDir, "cix.db"))
	// Loopback by default, unlike the server's own all-interfaces default. A
	// container has to be reachable from outside itself; a desktop app does not,
	// and exposing a code index to the local network is a choice someone should
	// make on purpose. The menu has a toggle for it.
	setDefault(vars, "CIX_BIND_ADDR", bindLocalOnly)
	// The .app owns updating itself. Leaving the server's own check on would
	// mean two different components offering the user two different "update
	// available" prompts for two different tag streams.
	setDefault(vars, "CIX_VERSION_CHECK_ENABLED", "false")

	if err := writeServerEnv(vars); err != nil {
		return fmt.Errorf("write server.env: %w", err)
	}

	if err := writeLaunchdFiles(false); err != nil {
		return fmt.Errorf("write launchd files: %w", err)
	}
	if err := startServer(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	// Register with the CLI so `cix` works out of the box. Failure here is not
	// fatal — the server is up and the dashboard works; only the terminal
	// client is left unconfigured, and the message says so.
	cliNote := ""
	if err := registerWithCLI(localBaseURL(vars), apiKey); err != nil {
		cliNote = fmt.Sprintf("\n\nThe cix command line client could not be configured automatically (%v). "+
			"The server itself is unaffected.", err)
	}

	waitNote := ""
	if err := waitForHealth(localBaseURL(vars), 90*time.Second); err != nil {
		// Not an error: a cold start loads an embedding model and can take
		// minutes, in silence. Saying so beats a spinner that gives up.
		waitNote = "\n\nThe server is still starting. First boot downloads and loads the embedding " +
			"model, which can take a few minutes; the menu bar will show it as running when it is ready."
	} else {
		// The account exists — bootstrap runs before the listener opens — so the
		// password that seeded it has done its job and stops being kept.
		retireBootstrapPassword()
	}

	return alertWithSecret("cix is set up", fmt.Sprintf(
		"Sign in at %s\n\nEmail:\n%s\n\nTemporary password:\n%s\n\n"+
			"You will be asked to change this password on first login.%s%s",
		dashboardURL(vars), email, password, waitNote, cliNote),
		password, "password")
}

// registerWithCLI adds (or updates) the local server in ~/.cix/config.yaml.
//
// It does not touch default_server when one is already set: a user whose CLI
// points at a remote cix should not have it silently repointed at localhost by
// installing an app.
func registerWithCLI(baseURL, apiKey string) error {
	if err := config.SetServerURL(bootstrapServerName, baseURL); err != nil {
		return err
	}
	if err := config.SetServerKey(bootstrapServerName, apiKey); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DefaultServer == "" {
		return config.SetDefaultServer(bootstrapServerName)
	}
	return nil
}

func waitForHealth(baseURL string, timeout time.Duration) error {
	c := client.New(baseURL, "")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := c.Health(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("server did not answer /health within %s", timeout)
}

// generateAPIKey mints a key in the server's own format: "cix_" plus 43
// base64url characters (32 random bytes). The format is not cosmetic — the
// server's ImportLegacy path stores this verbatim, and apikeys.PrefixDisplayLen
// assumes the prefix shape when rendering keys in the dashboard.
func generateAPIKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cix_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// generatePassword returns a 24-character base64url password.
//
// It is shown once, in a dialog, and the server forces a change at first login,
// so readability matters more than memorability. base64url avoids the quoting
// problem entirely: the value is written into a shell-sourced env file, and a
// generated password containing a quote would break the wrapper script.
func generatePassword() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
