//go:build windows

package stdio

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows the redirect must reach the OS-level standard handles too.
func checkHandles(t *testing.T) {
	t.Helper()
	for _, c := range []struct {
		id   uint32
		file *os.File
		name string
	}{
		{windows.STD_OUTPUT_HANDLE, os.Stdout, "stdout"},
		{windows.STD_ERROR_HANDLE, os.Stderr, "stderr"},
	} {
		h, err := windows.GetStdHandle(c.id)
		if err != nil {
			t.Fatalf("GetStdHandle(%s): %v", c.name, err)
		}
		if uintptr(h) != c.file.Fd() {
			t.Errorf("%s: OS handle %#x != os file handle %#x", c.name, uintptr(h), c.file.Fd())
		}
	}
}
