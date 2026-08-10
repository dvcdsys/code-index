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

// needsFirstRun reports whether ~/.cix/server.env is missing.
func needsFirstRun() bool {
	path, err := serverEnvPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// runFirstRun walks the user through creating the admin account, writes
// server.env, and registers the server with the CLI.
//
// Returns errCancelled if the user backs out, which is not an error condition —
// the app stays running with Start disabled until they complete setup.
func runFirstRun(b bundle) error {
	intro := "cix needs an administrator account before it can start.\n\n" +
		"Enter the email address to sign in with. A password will be generated for you, " +
		"and you will be asked to change it the first time you log in."

	email, err := prompt("Set up cix", intro, "")
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

	vars := map[string]string{
		"CIX_BOOTSTRAP_ADMIN_EMAIL":    email,
		"CIX_BOOTSTRAP_ADMIN_PASSWORD": password,
		// Imported as an "env-bootstrap" legacy key when the admin is created
		// on a fresh DB (bootstrap.go), so the CLI works from the first boot
		// without anyone visiting the dashboard to mint one.
		"CIX_API_KEY":     apiKey,
		"CIX_PORT":        strconv.Itoa(defaultServerPort),
		"CIX_DATA_DIR":    dataDir,
		"CIX_SQLITE_PATH": filepath.Join(dataDir, "cix.db"),
		// Loopback by default, unlike the server's own all-interfaces default.
		// A container has to be reachable from outside itself; a desktop app
		// does not, and exposing a code index to the local network is a choice
		// someone should make on purpose. The menu has a toggle for it.
		"CIX_BIND_ADDR": bindLocalOnly,
		// The .app owns updating itself. Leaving the server's own check on
		// would mean two different components offering the user two different
		// "update available" prompts for two different tag streams.
		"CIX_VERSION_CHECK_ENABLED": "false",
	}
	if err := writeServerEnv(vars); err != nil {
		return fmt.Errorf("write server.env: %w", err)
	}

	if err := writeLaunchdFiles(b, false); err != nil {
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
	}

	return alert("cix is set up", fmt.Sprintf(
		"Sign in at %s\n\nEmail:\n%s\n\nTemporary password:\n%s\n\n"+
			"You will be asked to change this password on first login.%s%s",
		dashboardURL(vars), email, password, waitNote, cliNote))
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
