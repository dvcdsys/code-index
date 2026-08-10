package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Launcher preferences: ~/.cix/launcher.json.
//
// Only decisions the user should not be asked twice about. Everything that
// configures the server lives in server.env, and everything about the launchd
// job lives in the plist; this file exists so the app can remember an answer,
// not so it can grow a second configuration system.

type prefs struct {
	// Takeover records what the user chose when an agent installed by
	// install-server.sh was found under the same launchd label: "takeover" or
	// "keep". Empty means the question has not been asked yet.
	Takeover string `json:"takeover,omitempty"`

	// UpdateETag and RuntimeETag are GitHub's ETags from the last listing of the
	// mac/v* and server/v* streams. Replaying one turns an unchanged check into
	// a 304, which does not count against the unauthenticated hourly limit — so
	// they are worth surviving a restart. Two streams, two ETags: the app and
	// the server it manages are released separately.
	UpdateETag  string `json:"update_etag,omitempty"`
	RuntimeETag string `json:"runtime_etag,omitempty"`
}

// There used to be a third field here, restart_server_after_update, carrying one
// bit across a launcher swap: the process that stopped the server was replaced
// before the one that should start it again existed. Moving the runtime out of
// the bundle removed the reason for it — an app update no longer touches the
// server at all, and a runtime update restarts it within a single process. An
// old file that still carries the key is simply ignored.

const (
	takeoverAdopt = "takeover"
	takeoverKeep  = "keep"
)

func prefsPath() (string, error) {
	dir, err := cixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "launcher.json"), nil
}

// loadPrefs returns the stored preferences, or zero values when the file is
// absent or unreadable. A corrupt prefs file must not stop the app starting:
// the worst case is being asked a question again.
func loadPrefs() prefs {
	path, err := prefsPath()
	if err != nil {
		return prefs{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("could not read %s: %v", path, err)
		}
		return prefs{}
	}
	var p prefs
	if err := json.Unmarshal(data, &p); err != nil {
		logf("could not parse %s: %v — treating as empty", path, err)
		return prefs{}
	}
	return p
}

func savePrefs(p prefs) error {
	path, err := prefsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
