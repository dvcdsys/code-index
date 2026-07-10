package watchtui

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/daemon"
)

// Item is one row in the manager: a known project (from the local config)
// and/or a running watcher daemon. Paths are resolved project roots, which
// is the common key between the config list, the daemon registry, and the
// server's host_path.
type Item struct {
	Path          string
	Running       bool
	PID           int
	AutoWatch     bool
	LastIndexedAt *time.Time
}

// Manager is the seam the TUI drives. Splitting it out keeps the daemon
// package free of a client import and lets tests inject a fake.
//
// List / Stop / StartAllAuto / StopAll / LogPath are fully local (no
// network). Start / Restart preflight against the server because the
// spawned watcher child needs a registered project to sync to.
// LastIndexed is the only network call and is best-effort enrichment.
type Manager interface {
	List() ([]Item, error)
	LastIndexed() (map[string]*time.Time, error)
	Stop(path string) error
	Start(path string) error
	Restart(path string) error
	StartAllAuto() (int, error)
	StopAll() (int, error)
	Delete(path string) error
	SetAutoWatch(path string, on bool) error
	LogPath(path string) (string, error)
}

// DaemonManager is the production Manager backed by the local config,
// the daemon registry, and (optionally) a cix server client.
type DaemonManager struct {
	client *client.Client
}

// NewDaemonManager builds a Manager. c may be nil (no server configured);
// listing and stopping still work, only starting requires a reachable
// server.
func NewDaemonManager(c *client.Client) *DaemonManager {
	return &DaemonManager{client: c}
}

// List merges the local known-project list with the running-daemon set.
// No network — safe to call on every keystroke/tick.
func (d *DaemonManager) List() ([]Item, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return mergeItems(cfg.Projects, daemon.ListAll()), nil
}

// LastIndexed returns path → last-indexed time from the server, for
// best-effort enrichment. A nil client or an unreachable server yields a
// nil map and no error — enrichment is optional, never fatal.
func (d *DaemonManager) LastIndexed() (map[string]*time.Time, error) {
	if d.client == nil {
		return nil, nil
	}
	projects, err := d.client.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*time.Time, len(projects))
	for _, p := range projects {
		out[p.HostPath] = p.LastIndexedAt
	}
	return out, nil
}

// Stop terminates the watcher for path.
func (d *DaemonManager) Stop(path string) error { return daemon.Stop(path) }

// StopAll terminates every running watcher.
func (d *DaemonManager) StopAll() (int, error) { return daemon.StopAll() }

// LogPath returns the log file path for path's watcher.
func (d *DaemonManager) LogPath(path string) (string, error) { return daemon.LogFilePath(path) }

// Start launches a watcher for path, gated by the server preflight.
func (d *DaemonManager) Start(path string) error {
	_, err := GuardedStart(d.client, path)
	return err
}

// Restart stops (if running) then starts the watcher for path. The
// preflight runs first so a server-down restart never leaves the user
// with nothing running.
func (d *DaemonManager) Restart(path string) error {
	if err := Preflight(d.client, path); err != nil {
		return err
	}
	if daemon.GetStatus(path).Running {
		if err := daemon.Stop(path); err != nil {
			return fmt.Errorf("stop old: %w", err)
		}
	}
	_, err := daemon.Start(path)
	return err
}

// StartAllAuto starts a watcher for every project flagged auto_watch that
// isn't already running. Returns the count started and the first error.
func (d *DaemonManager) StartAllAuto() (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("load config: %w", err)
	}
	started := 0
	var firstErr error
	for _, p := range cfg.Projects {
		if !p.AutoWatch || daemon.GetStatus(p.Path).Running {
			continue
		}
		if _, err := GuardedStart(d.client, p.Path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		started++
	}
	return started, firstErr
}

// SetAutoWatch flips the project's auto_watch flag in the local config.
// Purely local — no server involved. AddProject upserts, so toggling a
// running daemon whose path isn't yet in the config registers it too.
func (d *DaemonManager) SetAutoWatch(path string, on bool) error {
	return config.AddProject(path, on)
}

// Delete removes a project everywhere: it stops the watcher, deletes the
// project and its index on the server, then drops the local config entry and
// log. A server-side 404 (already deleted) is treated as success and the
// local cleanup still runs; any other server error aborts before touching
// local state so a live project is never orphaned.
func (d *DaemonManager) Delete(path string) error {
	if d.client == nil {
		return fmt.Errorf("no server configured — can't confirm server-side deletion")
	}
	// Stop the watcher first so a reindex can't re-create the project on the
	// server between the delete and the local cleanup.
	if daemon.GetStatus(path).Running {
		_ = daemon.Stop(path)
	}
	if err := d.client.DeleteProject(path); err != nil && !errors.Is(err, client.ErrProjectGone) {
		return err
	}
	if err := config.RemoveProject(path); err != nil {
		return fmt.Errorf("remove from config: %w", err)
	}
	if logPath, err := daemon.LogFilePath(path); err == nil {
		_ = os.Remove(logPath)
	}
	return nil
}

// Preflight verifies a watcher can be usefully started for path: a server
// must be reachable and the project must be registered on it (the watcher
// child exits immediately otherwise). Shared by the TUI and `cix watch`.
func Preflight(c *client.Client, path string) error {
	if c == nil {
		return fmt.Errorf("no server configured — set one with `cix config`")
	}
	if err := c.Health(); err != nil {
		return fmt.Errorf("server not reachable: %w", err)
	}
	if _, err := c.GetProject(path); err != nil {
		return fmt.Errorf("project not registered — run `cix init %s` first: %w", path, err)
	}
	return nil
}

// GuardedStart runs Preflight then starts the daemon, returning its PID.
func GuardedStart(c *client.Client, path string) (int, error) {
	if err := Preflight(c, path); err != nil {
		return 0, err
	}
	return daemon.Start(path)
}

// mergeItems unions the local known projects with the running daemons.
// Pure and deterministic: running rows first, then by path. Daemons whose
// path isn't in the config list are still surfaced. known carries the
// auto_watch flag. This is the unit-test anchor.
func mergeItems(known []config.ProjectEntry, daemons []daemon.Status) []Item {
	idx := make(map[string]int, len(known))
	items := make([]Item, 0, len(known))

	for _, p := range known {
		idx[p.Path] = len(items)
		items = append(items, Item{Path: p.Path, AutoWatch: p.AutoWatch})
	}
	for _, s := range daemons {
		if i, ok := idx[s.ProjectPath]; ok {
			items[i].Running = true
			items[i].PID = s.PID
			continue
		}
		idx[s.ProjectPath] = len(items)
		items = append(items, Item{Path: s.ProjectPath, Running: true, PID: s.PID})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Running != items[j].Running {
			return items[i].Running
		}
		return items[i].Path < items[j].Path
	})
	return items
}

// readTail returns the last n lines of a log file, or a single diagnostic
// line if it can't be read.
func readTail(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("(no log: %v)", err)}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{"(log is empty)"}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// humanAge renders a coarse relative time for the last-indexed column.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
