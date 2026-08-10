package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestEnv lays down a server.env like the wizard's, pointing at dbPath.
func writeTestEnv(t *testing.T, home, dbPath string, extra map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".cix"), 0o700); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"CIX_BOOTSTRAP_ADMIN_EMAIL":    "someone@example.com",
		"CIX_BOOTSTRAP_ADMIN_PASSWORD": "generated-once",
		"CIX_API_KEY":                  "cix_testkey",
		"CIX_PORT":                     "21847",
		"CIX_SQLITE_PATH":              dbPath,
		"CIX_BIND_ADDR":                bindLocalOnly,
	}
	for k, v := range extra {
		if v == "" {
			delete(vars, k)
			continue
		}
		vars[k] = v
	}
	if err := writeServerEnv(vars); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not really sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Setup is needed when the database is gone, not only when the config is.
//
// Keying on server.env alone meant that deleting ~/.cix/data left the app
// believing it was configured — and the server then minted a fresh admin from
// the bootstrap credentials still in that file, using the original generated
// password rather than whatever the user had set since.
func TestNeedsFirstRun(t *testing.T) {
	t.Run("no server.env", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if !needsFirstRun() {
			t.Error("needsFirstRun() = false with no server.env")
		}
	})

	t.Run("server.env but no database", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeTestEnv(t, home, filepath.Join(home, ".cix", "data", "cix.db"), nil)
		if !needsFirstRun() {
			t.Error("needsFirstRun() = false with a configured database that does not exist")
		}
	})

	t.Run("both present", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		db := filepath.Join(home, ".cix", "data", "cix.db")
		writeTestEnv(t, home, db, nil)
		touch(t, db)
		if needsFirstRun() {
			t.Error("needsFirstRun() = true on a complete installation")
		}
	})

	t.Run("hand-edited env with no database path", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeTestEnv(t, home, "", map[string]string{"CIX_SQLITE_PATH": ""})
		// Nothing to check against, so nothing to conclude. Running the wizard
		// here would overwrite a file somebody edited on purpose.
		if needsFirstRun() {
			t.Error("needsFirstRun() = true on an env file with no CIX_SQLITE_PATH")
		}
	})
}

func TestRetireBootstrapPassword(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db := filepath.Join(home, ".cix", "data", "cix.db")
	writeTestEnv(t, home, db, map[string]string{"CIX_BIND_ADDR": bindAllInterfaces})
	touch(t, db)

	retireBootstrapPassword()

	vars, err := readServerEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vars["CIX_BOOTSTRAP_ADMIN_PASSWORD"]; ok {
		t.Error("the bootstrap password survived")
	}
	// The email is what the reset-password dialog offers as a default, and it
	// is not a credential.
	if vars["CIX_BOOTSTRAP_ADMIN_EMAIL"] != "someone@example.com" {
		t.Errorf("email = %q, want it kept", vars["CIX_BOOTSTRAP_ADMIN_EMAIL"])
	}
	// Nothing else may be lost on the way through: the API key is what the CLI
	// authenticates with, and the bind address is a choice the user made.
	if vars["CIX_API_KEY"] != "cix_testkey" {
		t.Errorf("api key = %q, want it kept", vars["CIX_API_KEY"])
	}
	if vars["CIX_BIND_ADDR"] != bindAllInterfaces {
		t.Errorf("bind addr = %q, want it kept", vars["CIX_BIND_ADDR"])
	}

	// The file still holds an API key, so the mode still matters.
	path, _ := serverEnvPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != envFileMode {
		t.Errorf("server.env mode = %v, want %v", perm, os.FileMode(envFileMode))
	}

	// Idempotent: the menu calls this whenever it first sees a running server.
	retireBootstrapPassword()
	if vars, err := readServerEnv(); err != nil || vars["CIX_API_KEY"] != "cix_testkey" {
		t.Errorf("second call damaged the file: %v %v", vars, err)
	}
}

// The wizard runs again after a database is deleted, and on that path it must
// not quietly revert settings chosen since the first run — the network toggle
// most of all, because reverting it changes what the machine exposes.
func TestSetDefaultKeepsExistingChoices(t *testing.T) {
	vars := map[string]string{
		"CIX_BIND_ADDR": bindAllInterfaces,
		"CIX_PORT":      "  ", // whitespace is not a choice
	}
	setDefault(vars, "CIX_BIND_ADDR", bindLocalOnly)
	setDefault(vars, "CIX_PORT", "21847")
	setDefault(vars, "CIX_VERSION_CHECK_ENABLED", "false")

	if vars["CIX_BIND_ADDR"] != bindAllInterfaces {
		t.Errorf("bind addr = %q, want the existing choice kept", vars["CIX_BIND_ADDR"])
	}
	if vars["CIX_PORT"] != "21847" {
		t.Errorf("port = %q, want the blank value replaced", vars["CIX_PORT"])
	}
	if vars["CIX_VERSION_CHECK_ENABLED"] != "false" {
		t.Errorf("version check = %q, want the default filled in", vars["CIX_VERSION_CHECK_ENABLED"])
	}
}
