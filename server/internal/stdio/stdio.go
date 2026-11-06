// Package stdio redirects the process's standard output and error to files.
//
// It exists for supervisors that cannot redirect a child's streams themselves.
// launchd takes StandardOutPath/StandardErrorPath in the plist; Windows Task
// Scheduler has no equivalent, and wrapping the server in `cmd /c … >log 2>&1`
// would make the task's process a shell instead of cix-server (breaking pid
// tracking and the Ctrl+Break stop). So the server redirects itself, first
// thing, when started with -log-dir.
package stdio

import (
	"fmt"
	"os"
	"path/filepath"
)

// File names under the log directory. They match what the macOS launchd agent
// writes, so the launcher shows the same files on both platforms.
const (
	OutName = "cix-server.log"
	ErrName = "cix-server.err"
)

// Redirect creates dir if needed and points os.Stdout at <dir>/cix-server.log
// and os.Stderr at <dir>/cix-server.err, both opened for append. On Windows the
// process-level standard handles are replaced too (SetStdHandle), so anything
// that reads them directly — the Go runtime's crash output, a child that
// inherits the handles — lands in the same files and not in the void.
//
// Call it before anything logs: slog's default handler and log.Default bind
// os.Stderr when they are first used. The returned function restores the
// original streams and closes the files; production code never needs it, tests
// do.
func Redirect(dir string) (restore func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	outFile, err := open(OutName)
	if err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	errFile, err := open(ErrName)
	if err != nil {
		outFile.Close()
		return nil, fmt.Errorf("log dir: %w", err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	undo, err := installStd(outFile, errFile)
	if err != nil {
		outFile.Close()
		errFile.Close()
		return nil, fmt.Errorf("log dir: %w", err)
	}
	os.Stdout, os.Stderr = outFile, errFile

	return func() {
		os.Stdout, os.Stderr = origOut, origErr
		undo()
		outFile.Close()
		errFile.Close()
	}, nil
}
