package stdio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirectWritesBothStreamsAndAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs", "nested") // must be created

	restore, err := Redirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "to-stdout-1")
	fmt.Fprintln(os.Stderr, "to-stderr-1")
	restore()

	// A second run appends rather than truncating — the log survives restarts.
	restore, err = Redirect(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "to-stdout-2")
	fmt.Fprintln(os.Stderr, "to-stderr-2")
	checkHandles(t)
	restore()

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	if got, want := read(OutName), "to-stdout-1\nto-stdout-2\n"; got != want {
		t.Errorf("%s = %q, want %q", OutName, got, want)
	}
	if got, want := read(ErrName), "to-stderr-1\nto-stderr-2\n"; got != want {
		t.Errorf("%s = %q, want %q", ErrName, got, want)
	}
}

func TestRedirectRestoresOriginalStreams(t *testing.T) {
	origOut, origErr := os.Stdout, os.Stderr
	restore, err := Redirect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if os.Stdout == origOut || os.Stderr == origErr {
		t.Fatal("Redirect did not replace os.Stdout/os.Stderr")
	}
	restore()
	if os.Stdout != origOut || os.Stderr != origErr {
		t.Fatal("restore did not put the original streams back")
	}
}

func TestRedirectFailsWhenDirIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Redirect(filepath.Join(f, "sub")); err == nil {
		t.Fatal("expected an error when the log dir cannot be created")
	}
}
