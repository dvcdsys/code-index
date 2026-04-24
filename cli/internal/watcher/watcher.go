package watcher

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/fileutil"
	"github.com/anthropics/code-index/cli/internal/indexer"
	"github.com/rjeczalik/notify"
)

// Health states reported via stderr so users know whether the watcher is
// actually keeping the index up-to-date. Stored as int32 for atomic access.
const (
	healthHealthy int32 = 0 // indexing succeeded on its last attempt
	healthBroken  int32 = 1 // 3 retries failed; probe goroutine is polling for recovery
)

// healthProbeInterval is how often the watcher polls /health when it is in
// healthBroken state. On recovery it triggers a reindex of pending changes
// and flips back to healthy.
const healthProbeInterval = 30 * time.Second

// Watcher watches a project directory for file changes and triggers reindexing.
// Uses rjeczalik/notify which provides native OS file watching:
//   - macOS: FSEvents (1 FD for the entire recursive watch tree)
//   - Linux: inotify (1 FD per inotify instance)
type Watcher struct {
	projectPath      string
	apiClient        *client.Client
	debounceMS       int
	syncIntervalMins int
	excludeDirs      map[string]bool
	excludeExts      map[string]bool
	excludePatterns  []string // glob patterns matched against basename (e.g. "*.swp", ".#*", "4913")
	eventCh          chan notify.EventInfo
	logger           *log.Logger
	stopCh           chan struct{}
	mu               sync.Mutex
	pendingChanges   map[string]bool
	timer            *time.Timer
	firstEventAt     time.Time // when the current debounce window started — used by max-wait cap

	// Indexing state management
	indexingMu       sync.Mutex
	isIndexing       bool
	reindexRequested bool
	fullReindexReq   bool

	// health is read/written atomically from runIndexer and the probe goroutine.
	// See healthHealthy / healthBroken constants.
	health int32
}

// Options configures the watcher behavior.
type Options struct {
	DebounceMS       int
	SyncIntervalMins int
	ExcludeDirs      []string
	ExcludeExts      []string
	ExcludePatterns  []string // glob patterns matched against basename; defaults to DefaultExcludePatterns
	Logger           *log.Logger
}

// DefaultExcludeDirs are directories that should never be watched.
var DefaultExcludeDirs = []string{
	"node_modules", ".git", ".venv", "__pycache__",
	"dist", "build", ".next", ".cache", ".DS_Store",
	".idea", ".vscode", "vendor", "target", ".svn",
	".hg", "coverage", ".nyc_output", ".tox",
	".eggs", "*.egg-info", ".gradle", ".mvn",
}

// DefaultExcludeExts are file extensions that should be ignored. Editor
// atomic-write artefacts (`.swp` for vim, `.tmp`/`.bak` for many IDEs) are
// included because their Create+Remove churn caused the watcher to track and
// then fail to index transient files, surfacing as "missed" changes.
var DefaultExcludeExts = []string{
	".pyc", ".pyo", ".class", ".o", ".obj", ".exe",
	".dll", ".so", ".dylib", ".a", ".lib",
	".jpg", ".jpeg", ".png", ".gif", ".ico", ".svg",
	".mp3", ".mp4", ".avi", ".mov", ".wav",
	".zip", ".tar", ".gz", ".rar", ".7z",
	".woff", ".woff2", ".ttf", ".eot",
	".lock", ".sum",
	".swp", ".swx", ".swo", ".tmp", ".bak",
}

// DefaultExcludePatterns are filename globs matched against the basename of
// each event. Catches editor temp files that have no stable extension:
//   - `*~`   — Emacs/Vim backup suffix
//   - `.#*`  — Emacs lockfile
//   - `4913` — Vim atomic-write probe file
var DefaultExcludePatterns = []string{
	"*~", ".#*", "4913",
}

// New creates a new file watcher for the given project path.
func New(projectPath string, apiClient *client.Client, opts Options) (*Watcher, error) {
	if opts.DebounceMS <= 0 {
		opts.DebounceMS = 5000
	}

	if opts.SyncIntervalMins <= 0 {
		// 2 min fallback sync (was 5) — tighter safety net for events macOS
		// FSEvents may coalesce or drop under load.
		opts.SyncIntervalMins = 2
	}

	if opts.Logger == nil {
		opts.Logger = log.New(os.Stdout, "[watcher] ", log.LstdFlags)
	}

	excludeDirs := make(map[string]bool)
	dirs := opts.ExcludeDirs
	if len(dirs) == 0 {
		dirs = DefaultExcludeDirs
	}
	for _, d := range dirs {
		excludeDirs[d] = true
	}

	excludeExts := make(map[string]bool)
	exts := opts.ExcludeExts
	if len(exts) == 0 {
		exts = DefaultExcludeExts
	}
	for _, e := range exts {
		excludeExts[e] = true
	}

	excludePatterns := opts.ExcludePatterns
	if len(excludePatterns) == 0 {
		excludePatterns = DefaultExcludePatterns
	}

	return &Watcher{
		projectPath:      projectPath,
		apiClient:        apiClient,
		debounceMS:       opts.DebounceMS,
		syncIntervalMins: opts.SyncIntervalMins,
		excludeDirs:      excludeDirs,
		excludeExts:      excludeExts,
		excludePatterns:  excludePatterns,
		// 4096 buffer (was 256) to absorb bursts like `git checkout`, `npm
		// install`, `make`. Overflow is logged in Start() so users notice
		// dropped events instead of silent misses.
		eventCh:        make(chan notify.EventInfo, 4096),
		logger:         opts.Logger,
		stopCh:         make(chan struct{}),
		pendingChanges: make(map[string]bool),
	}, nil
}

// Start begins watching the project directory. Blocks until Stop is called.
func (w *Watcher) Start() error {
	w.logger.Printf("Watching %s (debounce: %dms)", w.projectPath, w.debounceMS)

	// Stale-session guard: a previous watcher that crashed between
	// /index/begin and /index/finish would leave an active session on the
	// server, so the first /index/begin here would 409. Idempotent call —
	// errors are ignored so older servers that don't implement /index/cancel
	// don't block startup.
	if resp, err := w.apiClient.CancelIndex(w.projectPath); err != nil {
		w.logger.Printf("stale-session guard: cancel skipped (%v)", err)
	} else if resp != nil && resp.Cancelled {
		w.logger.Println("stale-session guard: cancelled prior active session")
	}

	// Use "..." suffix for recursive watching.
	watchPath := filepath.Join(w.projectPath, "...")
	if err := notify.Watch(watchPath, w.eventCh, notify.All); err != nil {
		return fmt.Errorf("start watch: %w", err)
	}
	defer notify.Stop(w.eventCh)

	// Fallback: periodic incremental sync to catch anything missed by OS events.
	syncTicker := time.NewTicker(time.Duration(w.syncIntervalMins) * time.Minute)
	defer syncTicker.Stop()

	// Health probe: when health is `broken`, poll the server and retry the
	// pending changes once it recovers so the user does not need to restart
	// the watcher manually.
	healthTicker := time.NewTicker(healthProbeInterval)
	defer healthTicker.Stop()

	// Overflow detector: check the event-channel fill level on a short tick.
	// rjeczalik/notify silently drops events once the channel is full, which
	// is a primary cause of "watcher misses files" on macOS — so surface it.
	overflowTicker := time.NewTicker(2 * time.Second)
	defer overflowTicker.Stop()
	overflowWarned := false

	w.logger.Printf("Watching recursively via native OS events")

	// Initial sync on start to catch changes made while watcher was offline
	go w.triggerImmediateReindex("initial sync")

	for {
		select {
		case ei := <-w.eventCh:
			w.handleEvent(ei)

		case <-syncTicker.C:
			w.triggerImmediateReindex("periodic sync")

		case <-healthTicker.C:
			if atomic.LoadInt32(&w.health) == healthBroken {
				w.probeAndMaybeRecover()
			}

		case <-overflowTicker.C:
			fill := len(w.eventCh)
			threshold := cap(w.eventCh) * 3 / 4
			if fill >= threshold && !overflowWarned {
				fmt.Fprintf(os.Stderr,
					"[cix watch] WARN: event channel %d/%d full — OS events may be dropped. Consider adding the busy directory to exclude_patterns.\n",
					fill, cap(w.eventCh))
				w.logger.Printf("event channel near full: %d/%d", fill, cap(w.eventCh))
				overflowWarned = true
			} else if fill < cap(w.eventCh)/2 {
				overflowWarned = false // reset once it drains
			}

		case <-w.stopCh:
			w.logger.Println("Stopping watcher")
			return nil
		}
	}
}

// probeAndMaybeRecover is invoked by the health ticker while the watcher is
// in `broken` state. If /health responds OK, it flips the state back to
// healthy and re-runs indexing so pending changes land without the user
// having to restart.
func (w *Watcher) probeAndMaybeRecover() {
	if err := w.apiClient.Health(); err != nil {
		return // still down; keep polling
	}
	fmt.Fprintln(os.Stderr, "[cix watch] INDEXING RESTORED: server reachable, resuming reindex.")
	w.logger.Println("health restored, resuming indexing")
	atomic.StoreInt32(&w.health, healthHealthy)
	// Re-run indexing so pending changes (which accumulated while broken)
	// actually get sent.
	go w.runIndexer(false)
}

// Broken reports whether the watcher is currently in the `broken` health
// state. Callers (e.g. cmd/watch.go foreground mode) use this to set a
// non-zero exit code when Stop has been triggered mid-failure.
func (w *Watcher) Broken() bool {
	return atomic.LoadInt32(&w.health) == healthBroken
}

// Stop signals the watcher to stop.
func (w *Watcher) Stop() {
	close(w.stopCh)
}

// handleEvent processes a single notify event.
func (w *Watcher) handleEvent(ei notify.EventInfo) {
	path := ei.Path()

	// Skip excluded directories
	if w.isExcluded(path) {
		return
	}

	// Branch switch: .git/HEAD changed → immediate incremental reindex
	rel, _ := filepath.Rel(w.projectPath, path)
	if rel == filepath.Join(".git", "HEAD") {
		w.triggerImmediateReindex("git branch switched")
		return
	}

	// .gitignore, .cixignore, or .cixconfig.yaml changed → full reindex
	baseName := filepath.Base(path)
	if baseName == ".gitignore" || baseName == ".cixignore" || baseName == ".cixconfig.yaml" {
		w.triggerFullReindex()
		return
	}

	// Editor temp / atomic-write probe files — reject by basename pattern
	// before any os.Stat. Handles Emacs `.#foo`, Vim backup `foo~`, and
	// Vim's `4913` probe file that briefly appears during atomic writes.
	if w.isExcludedPattern(baseName) {
		return
	}

	// A missing path is expected on Remove/Rename events — those still need
	// to be tracked so the indexer learns the file is gone. Only when Stat
	// succeeds do we inspect the mode; otherwise we skip the directory and
	// IsRegular checks and rely on the extension filter below.
	info, statErr := os.Stat(path)
	if statErr == nil {
		if info.IsDir() {
			w.trackChange(path)
			return
		}
		// Non-regular files (symlink cycles, sockets, pipes, devices)
		// cannot be read like code files.
		if !info.Mode().IsRegular() {
			return
		}
	}

	// Skip non-code files by extension (fast path)
	if w.isExcludedExt(path) {
		return
	}

	// Binary detection requires reading the file — skip when the path is
	// gone (Remove/Rename). The extension filter above is the only gate for
	// those events, which matches the prior behaviour.
	if statErr == nil && fileutil.IsBinary(path) {
		return
	}

	w.trackChange(path)
}

// trackChange records a file change and resets the debounce timer.
//
// A continuous stream of events (build output, codegen, mass-rename) would
// otherwise reset the timer indefinitely and never flush. To bound latency
// we cap the total wait at 10×debounce from the first event of the current
// window: once that cap is hit, flush immediately even if events keep
// arriving.
func (w *Watcher) trackChange(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pendingChanges[path] = true

	// Start of a fresh debounce window — remember when it began.
	if w.firstEventAt.IsZero() {
		w.firstEventAt = time.Now()
	}

	// If the window has already exceeded the max-wait cap, flush right now
	// instead of extending it further. flushChanges itself clears
	// firstEventAt once it runs.
	maxWait := time.Duration(w.debounceMS) * time.Millisecond * 10
	if time.Since(w.firstEventAt) >= maxWait {
		if w.timer != nil {
			w.timer.Stop()
		}
		// Need to drop the lock before flushChanges, which re-acquires it.
		go w.flushChanges()
		return
	}

	// Normal path: reset or start debounce timer.
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(time.Duration(w.debounceMS)*time.Millisecond, func() {
		w.flushChanges()
	})
}

// triggerFullReindex triggers a full reindex (e.g. when .gitignore changes).
func (w *Watcher) triggerFullReindex() {
	// Cancel any pending incremental reindex
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.pendingChanges = make(map[string]bool)
	w.firstEventAt = time.Time{}
	w.mu.Unlock()

	w.logger.Println("Ignore rules changed (.gitignore/.cixignore), triggering full reindex...")
	w.runIndexer(true)
}

// triggerImmediateReindex cancels any pending debounce and immediately runs an
// incremental reindex.
func (w *Watcher) triggerImmediateReindex(reason string) {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.pendingChanges = make(map[string]bool)
	w.firstEventAt = time.Time{}
	w.mu.Unlock()

	w.logger.Printf("%s, triggering reindex...", reason)
	w.runIndexer(false)
}

// flushChanges sends accumulated changes to the API for reindexing.
func (w *Watcher) flushChanges() {
	w.mu.Lock()
	if len(w.pendingChanges) == 0 {
		w.firstEventAt = time.Time{}
		w.mu.Unlock()
		return
	}

	// Collect and reset pending changes
	changes := make([]string, 0, len(w.pendingChanges))
	for path := range w.pendingChanges {
		changes = append(changes, path)
	}
	w.pendingChanges = make(map[string]bool)
	w.firstEventAt = time.Time{}
	w.mu.Unlock()

	w.logger.Printf("Detected %d changed file(s), triggering incremental reindex...", len(changes))

	// Log changed files (up to 10)
	for i, path := range changes {
		if i >= 10 {
			w.logger.Printf("  ... and %d more", len(changes)-10)
			break
		}
		relPath, err := filepath.Rel(w.projectPath, path)
		if err != nil {
			relPath = path
		}
		w.logger.Printf("  %s", relPath)
	}

	w.runIndexer(false)
}

// runIndexer performs indexing sequentially, ensuring only one run at a time.
// If multiple requests arrive during indexing, they are coalesced into a
// single follow-up run.
func (w *Watcher) runIndexer(full bool) {
	w.mu.Lock()
	if w.isIndexing {
		w.reindexRequested = true
		if full {
			w.fullReindexReq = true
		}
		w.mu.Unlock()
		return
	}
	w.isIndexing = true
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			w.isIndexing = false
			reindex := w.reindexRequested
			isFull := w.fullReindexReq
			w.reindexRequested = false
			w.fullReindexReq = false
			w.mu.Unlock()

			if reindex {
				w.runIndexer(isFull)
			}
		}()

		// Run with transient failure retries
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			var result *indexer.Result
			result, err = indexer.Run(w.apiClient, w.projectPath, full, 0)
			if err == nil {
				if full {
					w.logger.Printf("Full reindex complete: %d files, %d chunks (run ID: %s)",
						result.FilesProcessed, result.ChunksCreated, result.RunID)
				} else {
					w.logger.Printf("Reindex complete: %d files, %d chunks (run ID: %s)",
						result.FilesProcessed, result.ChunksCreated, result.RunID)
				}
				// Recovery path: if we were broken, announce restoration.
				if atomic.CompareAndSwapInt32(&w.health, healthBroken, healthHealthy) {
					fmt.Fprintln(os.Stderr, "[cix watch] INDEXING RESTORED: reindex succeeded.")
				} else {
					atomic.StoreInt32(&w.health, healthHealthy)
				}
				return
			}
			w.logger.Printf("Indexing failed (attempt %d/3): %v", attempt+1, err)
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
			}
		}
		// All 3 attempts failed — surface to stderr so the user sees the
		// watcher is no longer keeping the index current. The health probe
		// ticker in Start() will retry automatically every 30s.
		if atomic.CompareAndSwapInt32(&w.health, healthHealthy, healthBroken) {
			fmt.Fprintf(os.Stderr,
				"[cix watch] INDEXING BROKEN: %v. Watcher will probe every %s and resume when the server responds. Check server logs.\n",
				err, healthProbeInterval)
		}
	}()
}

// isExcluded checks if a path should be ignored based on directory exclusions.
func (w *Watcher) isExcluded(path string) bool {
	rel, err := filepath.Rel(w.projectPath, path)
	if err != nil {
		return false
	}

	// Allow .git/HEAD through for branch switch detection.
	if rel == filepath.Join(".git", "HEAD") {
		return false
	}

	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if w.excludeDirs[part] {
			return true
		}
	}

	return false
}

// isExcludedExt checks if a file should be ignored based on its extension.
func (w *Watcher) isExcludedExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return w.excludeExts[ext]
}

// isExcludedPattern checks the basename against glob patterns used to skip
// editor temp / atomic-write artefacts (see DefaultExcludePatterns).
func (w *Watcher) isExcludedPattern(baseName string) bool {
	for _, pat := range w.excludePatterns {
		if ok, err := filepath.Match(pat, baseName); err == nil && ok {
			return true
		}
	}
	return false
}