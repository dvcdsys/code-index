package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Wording and small OS hooks the shared launcher code leans on. The Windows
// counterparts live in platform_windows.go.
const (
	thisMachine     = "this Mac"
	runtimeSizeHint = "about 40 MB"
	trayName        = "menu bar"
)

// openURL opens a URL in the user's default browser.
func openURL(url string) error { return exec.Command("open", url).Run() }

// trayIcon is the path to the menu bar glyph shipped inside the bundle.
func (b bundle) trayIcon() string { return filepath.Join(b.Resources, "cixTemplate.png") }

// processUptime asks ps for the elapsed time of a pid and renders it the way
// the design's header expects ("4h 12m"). Empty when anything goes wrong —
// an uptime is decoration, never worth an error dialog.
func processUptime(pid int) string {
	if pid == 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "etime=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return ""
	}
	return formatEtime(strings.TrimSpace(string(out)))
}
