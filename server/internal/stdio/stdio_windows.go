//go:build windows

package stdio

import (
	"os"

	"golang.org/x/sys/windows"
)

// installStd swaps the process's STD_OUTPUT_HANDLE / STD_ERROR_HANDLE. Setting
// os.Stdout alone only changes what Go code writes to; the Go runtime's own
// fatal-error output and any child that inherits the standard handles go
// through the Windows handles, and under Task Scheduler those start out as
// invalid.
func installStd(out, errf *os.File) (undo func(), err error) {
	prevOut, _ := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	prevErr, _ := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd())); err != nil {
		return nil, err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(errf.Fd())); err != nil {
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, prevOut)
		return nil, err
	}
	return func() {
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, prevOut)
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, prevErr)
	}, nil
}
