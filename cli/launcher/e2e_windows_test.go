package main

import (
	"os"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/client"
)

// An end-to-end check of the server supervision against the REAL machine: it
// drives the scheduled task, the process table and Ctrl+Break. Off unless
// CIX_WIN_E2E=1, and it needs an installation to drive — a runtime in
// ~\.cix\runtime and a server.env (the launcher's first-run produces both).
//
// It must run in the user's interactive session, not over SSH: AttachConsole
// cannot cross sessions. Register it as an interactive scheduled task.
func TestWindowsServerLifecycle(t *testing.T) {
	if os.Getenv("CIX_WIN_E2E") != "1" {
		t.Skip("set CIX_WIN_E2E=1 to run against the real installation")
	}
	if !runtimeReady() {
		t.Fatal("no runtime installed")
	}
	vars, err := readServerEnv()
	if err != nil {
		t.Fatal(err)
	}
	base := localBaseURL(vars)

	if err := writeServiceFiles(false); err != nil {
		t.Fatalf("writeServiceFiles: %v", err)
	}
	if !serviceLoaded() || foreignAgent() {
		t.Fatalf("task should be registered and ours (loaded=%v foreign=%v)", serviceLoaded(), foreignAgent())
	}
	if autostartEnabled() {
		t.Error("autostart should be off")
	}
	if err := setAutostart(true); err != nil || !autostartEnabled() {
		t.Errorf("setAutostart(true): %v enabled=%v", err, autostartEnabled())
	}
	if err := setAutostart(false); err != nil || autostartEnabled() {
		t.Errorf("setAutostart(false): %v enabled=%v", err, autostartEnabled())
	}

	start := time.Now()
	if err := startServer(); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && client.New(base, "").Health() != nil {
		time.Sleep(time.Second)
	}
	if err := client.New(base, "").Health(); err != nil {
		t.Fatalf("server not healthy after %s: %v\n%s", time.Since(start), err, lastServerError())
	}
	pid := servicePID()
	if pid == 0 {
		t.Fatal("servicePID() = 0 with a healthy server")
	}
	t.Logf("healthy after %s, pid %d, uptime %q", time.Since(start).Round(time.Second), pid, processUptime(pid))
	if serverDiedOnStart() != "" {
		t.Error("serverDiedOnStart reported a live server as dead")
	}

	stopAt := time.Now()
	if err := stopServer(); err != nil {
		t.Fatalf("stopServer: %v", err)
	}
	if servicePID() != 0 {
		t.Fatal("server still running after stopServer")
	}
	t.Logf("stopped in %s", time.Since(stopAt).Round(time.Millisecond))
	if time.Since(stopAt) > 25*time.Second {
		t.Error("stop took long enough to suggest the graceful path failed and it was terminated")
	}

	// And back up: the restart path menu.go uses.
	if err := startServer(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	for time.Now().Before(time.Now().Add(1)) && servicePID() == 0 {
		time.Sleep(200 * time.Millisecond)
		if time.Since(stopAt) > 90*time.Second {
			t.Fatal("server did not come back")
		}
	}
	_ = stopServer()
}
