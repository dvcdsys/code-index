package watcher

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/fileutil"
	"github.com/anthropics/code-index/cli/internal/indexer"
	"github.com/rjeczalik/notify"
)

// Watcher watches a project directory for file changes and triggers reindexing.
// Uses rjeczalik/notify which provides native OS file watching:
//   - macOS: FSEvents (1 FD for the entire recursive watch tree)
//   - Linux: inotify (1 FD per inotify instance)
type Watcher struct {
	projectPath    string
	apiClient      *client.Client
	debounceMS     int
	excludeDirs    map[string]bool
	excludeExts    map[string]bool
	eventCh        chan notify.EventInfo
	logger         *log.Logger
	stopCh         chan struct{}
	mu             sync.Mutex
	pendingChanges map[string]bool
	timer          *time.Timer
}

// Options configures the watcher behavior.
type Options struct {
	DebounceMS  int
	ExcludeDirs []string
	ExcludeExts []string
	Logger      *log.Logger
}

// DefaultExcludeDirs are directories that should never be watched.
var DefaultExcludeDirs = []string{
	"node_modules", ".git", ".venv", "__pycache__",
	"dist", "build", ".next", ".cache", ".DS_Store",
	".idea", ".vscode", "vendor", "target", ".svn",
	".hg", "coverage", ".nyc_output", ".tox",
	".eggs", "*.egg-info", ".gradle", ".mvn",
}

// DefaultExcludeExts are file extensions that should be ignored.
var DefaultExcludeExts = []string{
	".pyc", ".pyo", ".class", ".o", ".obj", ".exe",
	".dll", ".so", ".dylib", ".a", ".lib",
	".jpg", ".jpeg", ".png", ".gif", ".ico", ".svg",
	".mp3", ".mp4", ".avi", ".mov", ".wav",
	".zip", ".tar", ".gz", ".rar", ".7z",
	".woff", ".woff2", ".ttf", ".eot",
	".lock", ".sum",
}

// New creates a new file watcher for the given project path.
func New(projectPath string, apiClient *client.Client, opts Options) (*Watcher, error) {
	if opts.DebounceMS <= 0 {
		opts.DebounceMS = 5000
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

	return &Watcher{
		projectPath:    projectPath,
		apiClient:      apiClient,
		debounceMS:     opts.DebounceMS,
		excludeDirs:    excludeDirs,
		excludeExts:    excludeExts,
		eventCh:        make(chan notify.EventInfo, 256),
		logger:         opts.Logger,
		stopCh:         make(chan struct{}),
		pendingChanges: make(map[string]bool),
	}, nil
}

// Start begins watching the project directory. Blocks until Stop is called.
func (w *Watcher) Start() error {
	w.logger.Printf("Watching %s (debounce: %dms)", w.projectPath, w.debounceMS)

	// Use "..." suffix for recursive watching.
	// On macOS this uses FSEvents (1 FD for entire tree).
	// On Linux this uses inotify (1 FD per inotify instance).
	watchPath := filepath.Join(w.projectPath, "...")
	if err := notify.Watch(watchPath, w.eventCh, notify.All); err != nil {
		return fmt.Errorf("start watch: %w", err)
	}
	defer notify.Stop(w.eventCh)

	w.logger.Printf("Watching recursively via native OS events")

	for {
		select {
		case ei := <-w.eventCh:
			w.handleEvent(ei)

		case <-w.stopCh:
			w.logger.Println("Stopping watcher")
			return nil
		}
	}
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

	// .gitignore, .cixignore, or .cixconfig.yaml changed → full reindex
	baseName := filepath.Base(path)
	if baseName == ".gitignore" || baseName == ".cixignore" || baseName == ".cixconfig.yaml" {
		w.triggerFullReindex()
		return
	}

	// Skip directories (we only care about file changes for incremental reindex)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return
	}

	// Skip non-code files by extension (fast path)
	if w.isExcludedExt(path) {
		return
	}

	// Skip binary files by content detection (catches extensionless binaries)
	if fileutil.IsBinary(path) {
		return
	}

	w.trackChange(path)
}

// trackChange records a file change and resets the debounce timer.
func (w *Watcher) trackChange(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pendingChanges[path] = true

	// Reset or start debounce timer
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
	w.mu.Unlock()

	w.logger.Println("Ignore rules changed (.gitignore/.cixignore), triggering full reindex...")

	result, err := indexer.Run(w.apiClient, w.projectPath, true, 0)
	if err != nil {
		w.logger.Printf("Failed to trigger full reindex: %v", err)
		return
	}

	w.logger.Printf("Full reindex complete: %d files, %d chunks (run ID: %s)",
		result.FilesProcessed, result.ChunksCreated, result.RunID)
}

// flushChanges sends accumulated changes to the API for reindexing.
func (w *Watcher) flushChanges() {
	w.mu.Lock()
	if len(w.pendingChanges) == 0 {
		w.mu.Unlock()
		return
	}

	// Collect and reset pending changes
	changes := make([]string, 0, len(w.pendingChanges))
	for path := range w.pendingChanges {
		changes = append(changes, path)
	}
	w.pendingChanges = make(map[string]bool)
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

	result, err := indexer.Run(w.apiClient, w.projectPath, false, 0)
	if err != nil {
		w.logger.Printf("Failed to reindex: %v", err)
		return
	}

	w.logger.Printf("Reindex complete: %d files, %d chunks (run ID: %s)",
		result.FilesProcessed, result.ChunksCreated, result.RunID)
}

// isExcluded checks if a path should be ignored based on directory exclusions.
func (w *Watcher) isExcluded(path string) bool {
	// Check each component of the path
	rel, err := filepath.Rel(w.projectPath, path)
	if err != nil {
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