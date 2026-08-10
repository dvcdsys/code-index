package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Coexisting with install-server.sh.
//
// Both use the launchd label com.cix.server — install-server.sh:44 — and it
// points at a repo checkout (:351). So on a machine where someone already runs
// cix from a clone, this app finds an agent it did not create, pointing at a
// server it does not own, holding the port it would use.
//
// Silently rewriting that agent would repoint a working development install at
// the bundle without telling anyone, and re-running install-server.sh would
// silently take it back. Both directions are surprising. So the app asks once,
// remembers the answer, and defaults to leaving it alone.

// handleForeignAgent asks (once) what to do about an agent this app did not
// create, and acts on the answer. Returns true when the app now owns the agent.
func handleForeignAgent(b bundle) bool {
	p := loadPrefs()

	if p.Takeover == "" {
		existing := describeForeignAgent()
		adopt, err := ask("Another cix installation was found",
			"A cix-server background agent is already installed on this Mac, set up by "+
				"install-server.sh rather than by this app.\n\n"+
				existing+"\n\n"+
				"cix.app can take it over and manage it from the menu bar, or leave it "+
				"exactly as it is and only show its status.\n\n"+
				"If you take it over, re-running install-server.sh will claim it back.",
			"Take Over", "Leave It Alone")
		if err != nil {
			// Undecided is not a decision: ask again next launch rather than
			// recording a preference the user never expressed.
			logf("takeover prompt failed or was dismissed: %v", err)
			return false
		}
		p.Takeover = takeoverKeep
		if adopt {
			p.Takeover = takeoverAdopt
		}
		if err := savePrefs(p); err != nil {
			logf("could not save the takeover decision: %v", err)
		}
	}

	if p.Takeover != takeoverAdopt {
		logf("leaving the existing launchd agent alone (observe-only)")
		return false
	}

	if err := adoptForeignAgent(b); err != nil {
		logf("takeover failed: %v", err)
		_ = alert("Could not take over the existing installation",
			fmt.Sprintf("%v\n\nThe existing installation was left untouched. "+
				"cix will show its status but will not manage it.", err))
		// Forget the decision so a fixed environment can be retried, rather
		// than leaving the app permanently stuck between two modes.
		p.Takeover = ""
		_ = savePrefs(p)
		return false
	}
	return true
}

// adoptForeignAgent migrates the existing configuration, backs up what it
// replaces, and installs this app's wrapper and plist.
func adoptForeignAgent(b bundle) error {
	envPath, err := foreignEnvPath()
	if err != nil {
		return err
	}

	// Migrate first. Taking over an agent and then pointing it at a fresh,
	// empty database would look like the takeover destroyed the user's index.
	if envPath != "" {
		if err := migrateForeignEnv(envPath); err != nil {
			return fmt.Errorf("could not read the existing configuration at %s: %w", envPath, err)
		}
	} else if needsFirstRun() {
		return errors.New("the existing agent's configuration file could not be located, " +
			"so its database and API key cannot be carried over")
	}

	if err := backupForeignAgent(); err != nil {
		// Refuse rather than proceed: without a backup the previous setup is
		// not recoverable, and this is the one step that overwrites it.
		return fmt.Errorf("could not back up the existing agent: %w", err)
	}

	if err := launchdBootout(); err != nil {
		return fmt.Errorf("could not unload the existing agent: %w", err)
	}
	if err := writeLaunchdFiles(b, autostartEnabled()); err != nil {
		return err
	}
	if err := launchdBootstrap(); err != nil {
		return err
	}
	logf("took over the launchd agent previously managed by install-server.sh")
	return nil
}

// foreignEnvPath extracts the env file the existing wrapper sources.
//
// install-server.sh generates `source "<path>"` into its wrapper (:775-784).
// Parsing that beats guessing at a location, because the path points into
// whichever checkout the operator installed from.
func foreignEnvPath() (string, error) {
	wrapper, err := wrapperPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(wrapper)
	if err != nil {
		return "", nil // no wrapper to read; caller decides whether that is fatal
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "source ")
		if !ok {
			rest, ok = strings.CutPrefix(line, ". ")
		}
		if !ok {
			continue
		}
		path := strings.Trim(strings.TrimSpace(rest), `"'`)
		if path != "" {
			return path, nil
		}
	}
	return "", nil
}

// migrateForeignEnv copies the settings that identify the server's data into
// our own server.env.
//
// Only these four keys. The rest of an install-server.sh .env is that
// installation's business — model tuning, GPU layers, tunnel credentials — and
// copying it wholesale would silently import settings the user never chose for
// this app. These four are the ones that decide *which server* it is.
func migrateForeignEnv(path string) error {
	src, err := readEnvFile(path)
	if err != nil {
		return err
	}

	dst, err := readServerEnv()
	if err != nil {
		dst = map[string]string{}
	}
	for _, key := range []string{"CIX_PORT", "CIX_API_KEY", "CIX_SQLITE_PATH", "CIX_DATA_DIR"} {
		if v, ok := src[key]; ok && strings.TrimSpace(v) != "" {
			dst[key] = v
		}
	}
	// The app owns updating itself; leaving the server's own version check on
	// would mean two components offering two different update prompts.
	dst["CIX_VERSION_CHECK_ENABLED"] = "false"

	logf("migrated %d settings from %s", len(dst), path)
	return writeServerEnv(dst)
}

// backupForeignAgent copies the plist and wrapper aside before they are
// overwritten, so the previous setup can be restored by hand.
func backupForeignAgent() error {
	home, err := cixHome()
	if err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(home, "backup", "install-server-"+stamp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	plist, err := plistPath()
	if err != nil {
		return err
	}
	wrapper, err := wrapperPath()
	if err != nil {
		return err
	}
	for _, src := range []string{plist, wrapper} {
		data, err := os.ReadFile(src)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(src)), data, 0o600); err != nil {
			return err
		}
	}
	logf("backed up the previous agent to %s", dir)
	return nil
}

// describeForeignAgent renders what was found, for the prompt. Being concrete
// is what lets someone recognise their own installation instead of guessing.
func describeForeignAgent() string {
	var parts []string
	if envPath, _ := foreignEnvPath(); envPath != "" {
		parts = append(parts, "Configuration: "+envPath)
	}
	if wrapper, err := wrapperPath(); err == nil {
		if data, err := os.ReadFile(wrapper); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "exec "); ok {
					parts = append(parts, "Server: "+strings.Trim(strings.TrimSpace(rest), `"'`))
					break
				}
			}
		}
	}
	if len(parts) == 0 {
		return "Its configuration could not be read."
	}
	return strings.Join(parts, "\n")
}
