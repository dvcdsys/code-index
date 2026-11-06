//go:build windows

package procgroup

import "syscall"

// createNoWindow keeps a console-subsystem child (llama-server.exe) from
// popping up a console window when cix-server itself has none.
const createNoWindow = 0x08000000

func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
