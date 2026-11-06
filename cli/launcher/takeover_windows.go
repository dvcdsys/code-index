package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Another installation under our task name.
//
// macOS has a real case here — install-server.sh registers the same launchd
// label, so a developer's checkout and the app collide. Windows has no second
// installer, so what this guards against is narrower: a scheduled task called
// cix-server that this app did not create (a hand-rolled one, an older script).
// Clobbering it would silently repoint someone's working setup, so the app goes
// observe-only unless told otherwise — status and the dashboard link still work
// over HTTP, Start/Stop are disabled.

func handleForeignAgent(u *updater) bool {
	p := loadPrefs()

	if p.Takeover == "" {
		adopt, err := ask("Another cix installation was found",
			"A scheduled task named \"cix-server\" already exists on this PC, but it was not "+
				"created by this app.\n\n"+
				"cix can take it over and manage the server from the system tray, or leave it exactly as "+
				"it is and only show its status.\n\n"+
				"If you take it over, the existing task is backed up under ~\\.cix\\backup and replaced.",
			"Take Over", "Leave It Alone")
		if err != nil {
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
		logf("leaving the existing scheduled task alone (observe-only)")
		return false
	}

	if err := adoptForeignAgent(u); err != nil {
		logf("takeover failed: %v", err)
		_ = alert("Could not take over the existing installation",
			fmt.Sprintf("%v\n\nThe existing task was left untouched. cix will show its status but will not manage it.", err))
		p.Takeover = ""
		_ = savePrefs(p)
		return false
	}
	return true
}

// adoptForeignAgent backs the old task up, installs the runtime and registers
// our own task. There is no .env to migrate — the old task's configuration is
// unknowable — so a missing server.env sends the user through setup.
func adoptForeignAgent(u *updater) error {
	if err := ensureRuntime(u, logProgress); err != nil {
		return fmt.Errorf("could not install the cix server: %w", err)
	}
	if err := backupForeignAgent(); err != nil {
		return fmt.Errorf("could not back up the existing task: %w", err)
	}
	if err := stopServer(); err != nil {
		logf("could not stop the old server before takeover: %v", err)
	}
	if err := serviceBootout(); err != nil {
		return fmt.Errorf("could not remove the existing task: %w", err)
	}
	if needsFirstRun() {
		if err := runFirstRun(u); err != nil && !errors.Is(err, errCancelled) {
			return err
		}
		return nil
	}
	if err := writeServiceFiles(autostartEnabled()); err != nil {
		return err
	}
	logf("took over the scheduled task previously named %s", taskName)
	return nil
}

func backupForeignAgent() error {
	home, err := cixHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "backup", "task-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cix-server-task.xml"), []byte(taskXML()), 0o600)
}
