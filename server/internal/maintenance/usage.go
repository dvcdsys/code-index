package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Disk location ids. Stable strings — the dashboard keys its rows off them.
const (
	DiskSQLite = "sqlite"
	DiskChroma = "chroma"
	DiskRepos  = "repos"
	DiskGGUF   = "gguf"
)

// DiskUsage is one storage location the server owns.
type DiskUsage struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// UsedBytes is omitted rather than zeroed when the tree could not be
	// walked, so "unreadable" and "empty" stay distinguishable.
	UsedBytes    *int64 `json:"used_bytes,omitempty"`
	FSTotalBytes *int64 `json:"fs_total_bytes,omitempty"`
	FSFreeBytes  *int64 `json:"fs_free_bytes,omitempty"`
}

// VectorStoreUsage summarises the in-memory vector database. Every field here
// is read from chromem's process image, so this costs nothing — which is why
// the usage endpoint can report document counts even when it skips the
// directory walks.
type VectorStoreUsage struct {
	Collections int    `json:"collections"`
	Documents   int64  `json:"documents"`
	ActivePath  string `json:"active_namespace_path"`
}

// Usage is the "what is this server using right now" snapshot.
type Usage struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Memory      MemorySnapshot   `json:"memory"`
	Disks       []DiskUsage      `json:"disks"`
	VectorStore VectorStoreUsage `json:"vector_store"`
}

// Usage samples memory and walks the four storage locations.
//
// The walks are the expensive part — the vector store is one file per document
// and can hold hundreds of thousands of them, which measures at a few seconds
// of CPU. Concurrent callers therefore share one walk and one result, the same
// way Analyze does: two open dashboards, or a dashboard plus a script, would
// otherwise each walk the whole tree. The client-side staleTime does not help
// here because it is per browser tab.
func (s *Service) Usage(ctx context.Context) Usage {
	if u, ok := s.usage.get(s.now()); ok {
		return u
	}
	done, wait := s.usage.begin()
	if done == nil {
		select {
		case <-wait:
			if u, ok := s.usage.get(s.now()); ok {
				return u
			}
			// Leader produced nothing usable; fall through and compute it
			// ourselves rather than re-entering Usage. Same reasoning as
			// Analyze: recursing would add a stack frame per failed round.
		case <-ctx.Done():
			return Usage{GeneratedAt: s.now(), Memory: readMemory()}
		}
	} else {
		defer done()
	}

	out := s.computeUsage(ctx)
	// Only cache a complete answer. A walk cut short by a cancelled request
	// would otherwise pin wrong numbers for the whole TTL.
	if ctx.Err() == nil {
		s.usage.put(out, s.now())
	}
	return out
}

func (s *Service) computeUsage(ctx context.Context) Usage {
	out := Usage{
		GeneratedAt: s.now(),
		Memory:      readMemory(),
	}

	if vs := s.d.VectorStore; vs != nil {
		for _, c := range vs.ListCollections() {
			out.VectorStore.Collections++
			out.VectorStore.Documents += int64(c.Documents)
		}
		out.VectorStore.ActivePath = vs.BaseDir()
	}

	if s.d.Cfg == nil {
		return out
	}
	cfg := s.d.Cfg

	// SQLite must count -wal and -shm. The write-ahead log is routinely tens
	// of megabytes on a busy index and reporting the main file alone
	// understates the footprint.
	if cfg.SQLitePath != "" {
		d := DiskUsage{ID: DiskSQLite, Label: "SQLite database", Path: cfg.SQLitePath}
		if _, err := os.Stat(cfg.SQLitePath); err == nil {
			d.Exists = true
			total := fileSizeOrZero(cfg.SQLitePath) +
				fileSizeOrZero(cfg.SQLitePath+"-wal") +
				fileSizeOrZero(cfg.SQLitePath+"-shm")
			d.UsedBytes = &total
		}
		attachFS(&d, filepath.Dir(cfg.SQLitePath))
		out.Disks = append(out.Disks, d)
	}

	// The "chroma" row is the LIVE vector store — the SQLite databases under
	// VectorsDir. The legacy chromem tree is deliberately not a second row:
	// the disk id is an enumerated wire value the dashboard keys on, and the
	// pre-migration gob files are reported (and reclaimed) through the
	// abandoned-namespace category instead.
	if cfg.VectorsDir != "" {
		out.Disks = append(out.Disks, walkedDisk(ctx, DiskChroma, "Vector store", cfg.VectorsDir))
	} else if cfg.ChromaPersistDir != "" {
		out.Disks = append(out.Disks, walkedDisk(ctx, DiskChroma, "Vector store", cfg.ChromaPersistDir))
	}
	if root := s.reposRoot(); root != "" {
		out.Disks = append(out.Disks, walkedDisk(ctx, DiskRepos, "Cloned repositories", root))
	}
	if dir := s.activeGGUFCacheDir(); dir != "" {
		out.Disks = append(out.Disks, walkedDisk(ctx, DiskGGUF, "Model cache", dir))
	}

	return out
}

func walkedDisk(ctx context.Context, id, label, path string) DiskUsage {
	d := DiskUsage{ID: id, Label: label, Path: path}
	if _, err := os.Stat(path); err == nil {
		d.Exists = true
		if !ctxDone(ctx) {
			if n, ok := DirSizeBytes(ctx, path); ok {
				d.UsedBytes = &n
			}
		}
	}
	attachFS(&d, path)
	return d
}

// attachFS fills the volume capacity fields, walking up to the nearest
// existing ancestor so a not-yet-created directory still reports the volume it
// would land on.
func attachFS(d *DiskUsage, path string) {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if st, ok := diskStats(p); ok {
			total, free := st.TotalBytes, st.FreeBytes
			d.FSTotalBytes = &total
			d.FSFreeBytes = &free
			return
		}
		if parent := filepath.Dir(p); parent == p {
			return
		}
	}
}

// activeGGUFCacheDir is where cached model weights live.
//
// The provider is asked first, then config. The fallback matters: with
// embeddings disabled the provider reports nothing, and without it both the
// "Model cache" disk row and the whole unused-models category would vanish
// while the files sat on disk taking up gigabytes. Reporting a directory is
// safe on its own — nothing is deleted from it unless the ACTIVE MODEL is also
// known, which is the guard that actually protects the weights in use.
func (s *Service) activeGGUFCacheDir() string {
	if s.d.ActiveGGUFCacheDir != nil {
		if dir := s.d.ActiveGGUFCacheDir(); dir != "" {
			return dir
		}
	}
	if s.d.Cfg != nil {
		return s.d.Cfg.GGUFCacheDir
	}
	return ""
}

func (s *Service) activeChromaComponents() []string {
	if s.d.ActiveChromaComponents == nil {
		return nil
	}
	return s.d.ActiveChromaComponents()
}

func (s *Service) activeModelRepo() string {
	if s.d.ActiveModelRepo == nil {
		return ""
	}
	return s.d.ActiveModelRepo()
}

// activeNamespaceDirs lists every directory that belongs to the namespace
// currently being written to. An EMPTY result means "refuse to classify
// anything as stale" — guessing here means deleting the live index.
//
// There are normally two: the directory holding the live SQLite database, and
// the chromem directory it was imported from. The second one still holds the
// pre-migration gob files, which are the rollback path, so it has to be
// protected for as long as this namespace is the active one.
//
// The vector store's own answers are authoritative: they are literally the
// paths it was opened at. Reconstructing them from the provider's storage
// components is a second-guess that goes wrong exactly when it matters — with
// embeddings disabled the provider reports no components at all, yet the
// server still opened a real namespace (main.go falls back to a deterministic
// ollama-shaped one), and a mismatch there would put the live directory on the
// deletion list.
func (s *Service) activeNamespaceDirs() []string {
	var out []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		for _, existing := range out {
			if existing == dir {
				return
			}
		}
		out = append(out, dir)
	}
	if s.d.VectorStore != nil {
		add(s.d.VectorStore.BaseDir())
		add(s.d.VectorStore.LegacyChromaDir())
	}
	if len(out) > 0 {
		return out
	}
	comps := s.activeChromaComponents()
	if len(comps) == 0 || s.d.Cfg == nil {
		return nil
	}
	if s.d.Cfg.VectorsDir != "" {
		add(s.d.Cfg.VectorDirFor(comps))
	}
	if s.d.Cfg.ChromaPersistDir != "" {
		add(s.d.Cfg.ChromaDirFor(comps))
	}
	return out
}
