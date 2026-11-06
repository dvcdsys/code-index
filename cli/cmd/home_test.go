package cmd

import "testing"

// setTestHome points the user's home directory at dir for one test.
//
// os.UserHomeDir reads $HOME on Unix but %USERPROFILE% on Windows, so a test
// that only sets HOME silently keeps using the real profile there -- reading
// (and, for the config tests, writing) the developer's actual ~/.cix.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
