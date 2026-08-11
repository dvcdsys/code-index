// Package dbmaint owns SQLite compaction: reporting how much of the database
// file is wasted, reclaiming it, and the boot-time reconciliation that adopts
// a compacted copy.
//
// It is deliberately separate from internal/maintenance. That package is a
// pure analyze/clean service over the vector store and the filesystem, backed
// by an open database. This one replaces the database, restarts the process,
// and must be callable *before* the database is open — it runs from main()
// ahead of db.OpenWith.
//
// # Why the state lives in a file
//
// Compaction is a snapshot copy: SQLite's VACUUM INTO fixes the contents at
// the moment its read transaction opens, and anything committed afterwards is
// absent from the copy. Measured on a real 8.9 GB index, 18 200 rows were
// written during a 76 s copy and 850 of them reached the copy. Adopting such a
// copy would silently discard the rest, so the compactor freezes writes for
// the duration.
//
// That has a consequence for bookkeeping: the outcome of a compaction is only
// known *after* the snapshot it produced, and while it runs nothing may be
// written at all. A row in the database describing the operation could
// therefore neither be written during the run nor survive the swap. The
// journal is a file for that reason, not for convenience.
package dbmaint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phase is the durable step of an operation. The values are the wire enum of
// MaintenanceOperation.phase in doc/openapi.yaml.
//
// Only the phases up to PhaseSwapping are ever written before the process
// restarts; the rest are terminal and exist so the dashboard can report what
// happened once it comes back.
type Phase string

const (
	// PhaseIdle is the absence of an operation. It is never persisted — a
	// missing journal means idle — but it is what the API reports.
	PhaseIdle Phase = "idle"
	// PhasePreparing is draining background work. Nothing has been touched
	// yet and the operation can still be abandoned for free.
	PhasePreparing Phase = "preparing"
	// PhaseCopying is the read-only window: the copy is being written.
	PhaseCopying Phase = "copying"
	// PhaseReadyToSwap means the copy is complete, fsynced and verified.
	// This is the only phase on which the reconciler will adopt a copy.
	PhaseReadyToSwap Phase = "ready_to_swap"
	// PhaseSwapping means the reconciler has begun renaming files. Which
	// rename has landed is answered by the files on disk, not by this value.
	PhaseSwapping Phase = "swapping"
	// PhaseRestarting is set just before the process re-executes itself.
	PhaseRestarting Phase = "restarting"

	PhaseDone   Phase = "done"
	PhaseFailed Phase = "failed"
	// PhaseInterrupted is a run that did not finish. The database was brought
	// back to a consistent state; it is a report, not an error to clear.
	PhaseInterrupted Phase = "interrupted"
)

// terminalPhases are the phases that carry no intent — the reconciler treats
// them the same as a missing journal when deciding what to do.
func (p Phase) terminal() bool {
	switch p {
	case PhaseIdle, PhaseDone, PhaseFailed, PhaseInterrupted, "":
		return true
	}
	return false
}

// Kind names the operation. Only KindCompact is journalled — reclaim and
// checkpoint are synchronous, bounded and need no crash recovery — but the
// wire type carries all three.
type Kind string

const (
	KindCompact    Kind = "compact"
	KindReclaim    Kind = "reclaim"
	KindCheckpoint Kind = "checkpoint"
)

// Level is an event severity.
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Event is one line of the operation's trail.
type Event struct {
	At      time.Time `json:"at"`
	Level   Level     `json:"level"`
	Phase   Phase     `json:"phase"`
	Message string    `json:"message"`
	RunID   string    `json:"run_id,omitempty"`
}

// Fingerprint is the cheap identity of a database file, used to prove a copy
// is the database it claims to be before anything irreversible happens to the
// original. Counts are read from the source under the write freeze and from
// the copy afterwards; because writes are frozen the two must agree exactly.
type Fingerprint struct {
	Users     int64 `json:"users"`
	Projects  int64 `json:"projects"`
	APIKeys   int64 `json:"api_keys"`
	SchemaVer int64 `json:"schema_version"`
	PageCount int64 `json:"page_count"`
	PageSize  int64 `json:"page_size"`
}

// Equal reports whether two fingerprints describe the same content. Page
// geometry is deliberately excluded: compaction changes page_count, which is
// the entire point.
func (f Fingerprint) Equal(o Fingerprint) bool {
	return f.Users == o.Users &&
		f.Projects == o.Projects &&
		f.APIKeys == o.APIKeys &&
		f.SchemaVer == o.SchemaVer
}

// State is the journal: everything a fresh process needs to know about an
// operation that a previous process may not have survived.
type State struct {
	RunID      string     `json:"run_id"`
	Kind       Kind       `json:"kind"`
	Phase      Phase      `json:"phase"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// PID and Version identify the process that wrote this. A journal naming
	// a pid that is not us, in a non-terminal phase, is by definition stale:
	// its author did not survive.
	PID     int    `json:"pid"`
	Version string `json:"version,omitempty"`

	BytesTotal int64 `json:"bytes_total,omitempty"`
	BytesDone  int64 `json:"bytes_done,omitempty"`
	FreedBytes int64 `json:"freed_bytes,omitempty"`

	EnableIncremental bool `json:"enable_incremental,omitempty"`

	// Source is read under the freeze; Copy is read back from the finished
	// copy. Both are recorded so the reconciler can re-verify at boot without
	// trusting anything it did not measure itself.
	Source *Fingerprint `json:"source,omitempty"`
	Copy   *Fingerprint `json:"copy,omitempty"`

	// ThroughputBytesPerSec is the measured copy rate, fed back into the next
	// run's estimate so it stops being a guess after the first compaction.
	ThroughputBytesPerSec int64 `json:"throughput_bytes_per_sec,omitempty"`

	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`

	// Events is a trailing window for the API. The full trail is in the log
	// file beside the journal.
	Events []Event `json:"events,omitempty"`
}

const (
	journalName = "maintenance.json"
	logName     = "maintenance.log"

	// maxJournalEvents bounds what the journal carries. The log file keeps
	// everything; this is only what the dashboard renders.
	maxJournalEvents = 50
)

// JournalPath is the journal for a database at dbPath. It lives beside the
// database so it shares a filesystem with it — the reconciler's reasoning
// depends on renames being atomic and on "which files exist" being a
// meaningful question.
func JournalPath(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), journalName) }

// LogPath is the append-only event trail beside the database.
func LogPath(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), logName) }

// Load reads the journal. A missing journal is not an error: it returns
// (State{Phase: PhaseIdle}, false, nil).
//
// A journal that cannot be parsed is renamed aside rather than deleted — it is
// the only evidence of whatever went wrong — and reported as absent, so the
// reconciler falls back to deciding from the files on disk. That fallback is
// safe by construction: the file set, not the journal, is what authorises
// every destructive step.
func Load(dbPath string) (State, bool, error) {
	p := JournalPath(dbPath)
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Phase: PhaseIdle}, false, nil
		}
		return State{Phase: PhaseIdle}, false, fmt.Errorf("read journal: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		bad := p + ".bad-" + time.Now().UTC().Format("20060102T150405Z")
		if rerr := os.Rename(p, bad); rerr != nil {
			return State{Phase: PhaseIdle}, false, fmt.Errorf(
				"journal at %s is corrupt (%v) and could not be set aside: %w", p, err, rerr)
		}
		return State{Phase: PhaseIdle}, false, fmt.Errorf(
			"journal at %s was corrupt and has been kept as %s: %w", p, bad, err)
	}
	if st.Phase == "" {
		st.Phase = PhaseIdle
	}
	return st, true, nil
}

// Save writes the journal atomically: a temporary file in the same directory,
// fsynced, renamed over the target, then the directory itself fsynced.
//
// The directory fsync is not ceremony. Without it a power loss can leave the
// rename unpersisted while the file contents are on disk, and the reconciler
// would then be reasoning about a file set that does not durably exist.
func Save(dbPath string, st State) error {
	p := JournalPath(dbPath)
	dir := filepath.Dir(p)
	if len(st.Events) > maxJournalEvents {
		st.Events = st.Events[len(st.Events)-maxJournalEvents:]
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, journalName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create journal temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write journal temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync journal temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close journal temp: %w", err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("rename journal into place: %w", err)
	}
	return SyncDir(dir)
}

// Clear removes the journal. Used when an operation reaches a terminal state
// that carries nothing worth reporting, and after the reconciler has finished
// acting on one.
func Clear(dbPath string) error {
	if err := os.Remove(JournalPath(dbPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove journal: %w", err)
	}
	return SyncDir(filepath.Dir(JournalPath(dbPath)))
}

// SyncDir fsyncs a directory so that renames and unlinks within it are
// durable. Exported because the swap performs its own renames and needs the
// same guarantee.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir for sync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}
	return nil
}

// AppendEvent adds a line to the trail beside the database.
//
// Best-effort by design and by signature: the caller gets an error but is
// expected to log and continue. Losing a log line must never abort a
// compaction, and an operation that spans a process restart has no other
// place to leave a record — the server log is split across two lifetimes and
// the interesting half is usually the one that did not come back.
func AppendEvent(dbPath string, ev Event) error {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Level == "" {
		ev.Level = LevelInfo
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	f, err := os.OpenFile(LogPath(dbPath), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

// ReadEvents returns the most recent n events from the trail, oldest first.
// A malformed line is skipped rather than failing the read: the log is
// diagnostic, and one bad line must not hide the rest.
func ReadEvents(dbPath string, n int) ([]Event, error) {
	raw, err := os.ReadFile(LogPath(dbPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read event log: %w", err)
	}
	var out []Event
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			if i > start {
				var ev Event
				if json.Unmarshal(raw[start:i], &ev) == nil {
					out = append(out, ev)
				}
			}
			start = i + 1
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
