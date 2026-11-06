package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Wording and small OS hooks the shared launcher code leans on. The macOS
// counterparts live in platform_darwin.go.
const (
	thisMachine     = "this PC"
	runtimeSizeHint = "about 500 MB"
	trayName        = "system tray"
)

// openURL opens a URL in the user's default browser.
func openURL(url string) error {
	r, _, err := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(wstr("open"))),
		uintptr(unsafe.Pointer(wstr(url))), 0, 0, 1)
	if r <= 32 { // ShellExecute returns a value > 32 on success
		return fmt.Errorf("ShellExecute: %v", err)
	}
	return nil
}

// trayIcon is unused on Windows: the glyph is embedded and recoloured for the
// taskbar theme (see buildTrayIcon).
func (b bundle) trayIcon() string { return "" }

// hiddenCommand runs a console tool without flashing a window. A GUI app that
// starts schtasks.exe would otherwise pop a console for every poll.
func hiddenCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// runHidden runs a tool with a deadline and returns its combined output.
func runHidden(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := hiddenCommand(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// processUptime renders how long a process has been alive the way the panel's
// header expects ("4h 12m"); empty when anything goes wrong.
func processUptime(pid int) string {
	if pid == 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return ""
	}
	return formatUptime(time.Since(time.Unix(0, created.Nanoseconds())))
}

// formatUptime is the shared two-unit rendering ("2d 3h", "4h 12m", "7m").
func formatUptime(d time.Duration) string {
	mins := int(d.Minutes())
	h, m := mins/60, mins%60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// cixDirPath joins under ~/.cix using the platform separator.
func cixDirPath(parts ...string) (string, error) {
	dir, err := cixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}

func formatUptimeMin(m int) string { return formatUptime(time.Duration(m) * time.Minute) }
