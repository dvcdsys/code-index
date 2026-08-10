package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A file log, next to the server's own logs.
//
// The launcher has no terminal — it is an LSUIElement app — so anything it
// writes to stderr is lost. That is fine for chatter and not fine for the
// detail behind a failure, which is exactly what someone needs when the menu
// says "could not start the server" and nothing else.
//
// It is also the sink for output that must NOT reach a dialog. `cix-server
// -reset-password` answers an unknown address by listing every account in the
// database (resetpassword.go: "existing users: …"). That is a reasonable thing
// to print for an operator who already holds the DB file; putting it in a GUI
// alert would turn a typo into an account enumeration anyone standing behind
// the user can read.

var (
	logMu   sync.Mutex
	logFile *os.File
)

func launcherLogPath() (string, error) {
	dir, err := logDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "launcher.log"), nil
}

// logf appends a timestamped line. Best effort: a launcher that cannot write
// its log still has a job to do, so failures here are silent by design.
func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()

	if logFile == nil {
		path, err := launcherLogPath()
		if err != nil {
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		logFile = f
	}

	fmt.Fprintf(logFile, "%s  %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
