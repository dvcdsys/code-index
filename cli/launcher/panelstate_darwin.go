package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// panelState is the one object panel.html renders from. Field names are the
// contract with the JavaScript side; change them in both places or not at all.
type panelState struct {
	State     string `json:"state"` // "running" | "starting" | "stopped"
	Busy      bool   `json:"busy"`
	Managed   bool   `json:"managed"`
	Port      int    `json:"port"`
	PID       int    `json:"pid,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
	LocalOnly bool   `json:"localOnly"`
	LanAddr   string `json:"lanAddr,omitempty"`
	Autostart bool   `json:"autostart"`

	Engine        string `json:"engine,omitempty"`
	EngineReady   bool   `json:"engineReady"`
	Model         string `json:"model,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`

	// What is on disk, shown when nothing is answering.
	Runtime    string `json:"runtime,omitempty"`
	AppVersion string `json:"appVersion,omitempty"`

	IndexingJobs int `json:"indexingJobs"`
	Projects     int `json:"projects"`
}

// buildPanelState folds a poller snapshot and the menu's own busy flag into
// what the panel shows.
func buildPanelState(s snapshot, busy bool) panelState {
	ps := panelState{
		Busy:       busy,
		Managed:    s.Managed,
		Port:       s.Port,
		PID:        s.PID,
		LocalOnly:  s.LocalOnly,
		Autostart:  s.Autostart,
		Runtime:    currentRuntimeVersion(),
		AppVersion: displayVersion(),
	}

	switch s.State {
	case stateRunning:
		ps.State = "running"
		ps.Uptime = processUptime(s.PID)
	case stateStarting:
		ps.State = "starting"
	default:
		ps.State = "stopped"
	}

	if !s.LocalOnly {
		ps.LanAddr = lanAddr(s.Port)
	}

	if s.Status != nil {
		ps.Engine = providerLabel(s.Status.EmbeddingProvider)
		ps.EngineReady = s.EmbeddingsOK
		ps.Model = s.ModelName()
		ps.ServerVersion = s.Status.ServerVersion
		ps.IndexingJobs = s.Status.ActiveIndexingJobs
		ps.Projects = s.Status.Projects
	}

	return ps
}

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

// formatEtime converts ps's [[dd-]hh:]mm:ss into a two-unit human string.
// Split out from processUptime so the parsing is testable without a process.
func formatEtime(etime string) string {
	if etime == "" {
		return ""
	}
	days := 0
	if d, rest, ok := strings.Cut(etime, "-"); ok {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil {
			return ""
		}
		etime = rest
	}
	parts := strings.Split(etime, ":")
	nums := make([]int, 0, 3)
	for _, p := range parts {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil {
			return ""
		}
		nums = append(nums, n)
	}

	var h, m int
	switch len(nums) {
	case 3:
		h, m = nums[0], nums[1]
	case 2:
		m = nums[0]
	default:
		return ""
	}
	h += days * 24

	// Two units at most: "2d 3h", "4h 12m", "7m". Seconds are noise on an
	// uptime and the panel repolls anyway.
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// lanAddr names the address the server is reachable at from other machines —
// the consequence the network toggle's hint line exists to state. Best-effort:
// the first non-loopback IPv4 is the address a home network knows this Mac by.
func lanAddr(port int) string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return fmt.Sprintf("%s:%d", ip4, port)
		}
	}
	return ""
}
