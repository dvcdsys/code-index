//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The platform-neutral half of server supervision. Each platform supplies the
// primitives — serviceLoaded, servicePID, startServer, stopServer,
// writeServiceFiles, autostartEnabled, setAutostart, foreignAgent — in
// launchd_darwin.go (launchd agent) or service_windows.go (Task Scheduler task).
// What is built on top of them is the same on both.

func logDir() (string, error) {
	dir, err := cixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// serverDiedOnStart reports why the server is already gone after a Start we
// asked for, or "" when it is still alive.
//
// launchctl answers for having spawned the process, not for the process
// surviving it. A server that rejects its own configuration exits in
// milliseconds and leaves the menu reading "Stopped" with no explanation —
// indistinguishable from a Start button that does nothing.
//
// The wait is two-sided on purpose: a pid takes a moment to appear, and a
// process that is going to die does it almost at once. Neither half is a
// deadline for anything the user waits on — a healthy server returns from here
// as soon as it has a pid.
func serverDiedOnStart() string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && servicePID() == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	if servicePID() != 0 {
		return ""
	}
	return lastServerError()
}

// lastServerError pulls the tail of cix-server.err, for a dialog.
//
// The server writes its refusals there as plain prose across several lines, so
// the last few non-empty ones are the message. Capped: an unbounded log tail in
// a modal dialog is its own failure.
func lastServerError() string {
	dir, err := logDir()
	if err != nil {
		return "The server exited immediately."
	}
	data, err := os.ReadFile(filepath.Join(dir, "cix-server.err"))
	if err != nil {
		return "The server exited immediately."
	}

	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " \t"))
		}
	}
	if len(lines) == 0 {
		return "The server exited immediately, without logging anything."
	}
	const keep = 8
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	out := strings.Join(lines, "\n")
	const maxRunes = 900
	if r := []rune(out); len(r) > maxRunes {
		out = "…" + string(r[len(r)-maxRunes:])
	}
	return out
}
