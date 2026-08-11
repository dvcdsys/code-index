package dbmaint

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dvcdsys/code-index/server/internal/storage"
)

// File suffixes for the two extra files a compaction can leave behind.
const (
	copySuffix = ".compact"
	oldSuffix  = ".old"
)

// Paths names the three files the reconciler reasons about.
type Paths struct {
	Live string // the database the server opens
	Copy string // a compacted candidate, complete or partial
	Old  string // the displaced original, kept until the swap is confirmed
}

// PathsFor derives the file set from the configured database path.
func PathsFor(dbPath string) Paths {
	return Paths{Live: dbPath, Copy: dbPath + copySuffix, Old: dbPath + oldSuffix}
}

// action is what the reconciler has decided to do. Split from the doing so the
// decision table can be tested exhaustively without touching a filesystem —
// this table is the only thing standing between an interrupted compaction and
// a lost database, and every row of it needs a test.
type action int

const (
	actNothing action = iota
	// actMarkInterrupted: a run died without touching any file.
	actMarkInterrupted
	// actAdoptCopy: verify the candidate and run the swap. The only path that
	// replaces the live database.
	actAdoptCopy
	// actDiscardPartialCopy: the copy was still being written.
	actDiscardPartialCopy
	// actDiscardOrphanCopy: a copy with no run behind it.
	actDiscardOrphanCopy
	// actKeepBothWarn: live, copy and displaced original all present — not
	// reachable through the protocol, so somebody moved files by hand.
	actKeepBothWarn
	// actResumeSwap: the live name is free and the candidate is waiting.
	actResumeSwap
	// actPromoteCopy: the candidate is the only database present.
	actPromoteCopy
	// actRollback: the candidate is gone and only the displaced original
	// remains.
	actRollback
	// actFinishCleanup: the swap landed, only the old file's removal did not.
	actFinishCleanup
	// actAmbiguousOld: a live database and a displaced original, with no
	// journal evidence of which is which. Touch nothing.
	actAmbiguousOld
	// actAllGone: every database is missing mid-operation.
	actAllGone
)

// authorisesSwap reports whether a phase means "a candidate copy is waiting to
// be adopted".
//
// PhaseSwapping is included deliberately: the journal is written *before* the
// first rename, so a crash between the two leaves the phase at swapping with
// the files still in their pre-swap positions. Resuming forward from there is
// the same operation. PhaseRestarting is included because it is only ever
// written after PhaseReadyToSwap and means exactly the same thing.
func (p Phase) authorisesSwap() bool {
	switch p {
	case PhaseReadyToSwap, PhaseSwapping, PhaseRestarting:
		return true
	}
	return false
}

// inProgress reports a phase where the copy was still being produced.
func (p Phase) inProgress() bool {
	return p == PhasePreparing || p == PhaseCopying
}

// decide maps (which files exist, what the journal claims) onto exactly one
// action.
//
// The governing rule: **the file set authorises, the journal only explains.**
// Every destructive branch is reachable only when the files themselves prove
// it is safe, so a journal that is missing, stale or corrupt can never cause
// data loss — at worst it costs a re-run.
func decide(hasLive, hasCopy, hasOld bool, phase Phase) action {
	switch {
	case hasLive && !hasCopy && !hasOld:
		if phase.terminal() {
			return actNothing
		}
		// A run died before producing anything, or its copy is gone. Either
		// way the live database was never touched.
		return actMarkInterrupted

	case hasLive && hasCopy && !hasOld:
		switch {
		case phase.authorisesSwap():
			return actAdoptCopy
		case phase.inProgress():
			return actDiscardPartialCopy
		default:
			return actDiscardOrphanCopy
		}

	case hasLive && hasCopy && hasOld:
		// Unreachable through the protocol: the copy is consumed before the
		// original is displaced. The live file is a complete database, so boot
		// on it, drop the candidate, and leave the original alone for whoever
		// put it there.
		return actKeepBothWarn

	case !hasLive && hasCopy && hasOld:
		// Interrupted between displacing the original and moving the candidate
		// into place. The candidate is complete (it was verified before the
		// first rename) and the original is intact, so going forward and going
		// back are both safe — go forward, because that is what was asked for.
		return actResumeSwap

	case !hasLive && hasCopy && !hasOld:
		// Only the candidate exists. Not reachable through the protocol, but
		// it is the only database present and refusing to use it would leave
		// the server with none.
		return actPromoteCopy

	case !hasLive && !hasCopy && hasOld:
		// The candidate was lost after the original was displaced.
		return actRollback

	case hasLive && !hasCopy && hasOld:
		if phase == PhaseSwapping || phase == PhaseDone {
			return actFinishCleanup
		}
		// Without journal evidence there is no way to tell whether the live
		// file is the new database or the untouched original, and the two
		// remedies are opposites. Do nothing and say so.
		return actAmbiguousOld

	default: // nothing at all
		if phase.terminal() {
			return actNothing // fresh install
		}
		return actAllGone
	}
}

// Reconcile brings the database file set back to a single consistent database
// and, when a verified compacted copy is waiting, adopts it.
//
// It must run before anything opens the database — from main() ahead of
// db.OpenWith, in the same slot as storage.AdoptLegacyModelDB, and from the
// offline password-reset path, which would otherwise open the wrong file.
// Running it with the database open is not merely unsupported; it renames the
// file out from under the open handle.
//
// It never returns an error for a state it resolved, only for one it could
// not. A returned error means the server should not start.
func Reconcile(ctx context.Context, dbPath string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	p := PathsFor(dbPath)

	st, _, loadErr := Load(dbPath)
	if loadErr != nil {
		// A corrupt journal has already been set aside by Load; carry on and
		// decide from the files, which is what actually authorises anything.
		logger.Warn("database maintenance journal unreadable, deciding from files on disk",
			"err", loadErr)
	}

	hasLive, hasCopy, hasOld := exists(p.Live), exists(p.Copy), exists(p.Old)
	act := decide(hasLive, hasCopy, hasOld, st.Phase)
	if act == actNothing {
		return nil
	}

	logger.Info("reconciling database maintenance state",
		"phase", string(st.Phase), "run_id", st.RunID,
		"live", hasLive, "copy", hasCopy, "displaced_original", hasOld)

	switch act {
	case actMarkInterrupted:
		return finishInterrupted(dbPath, st, logger,
			"a compaction did not finish; the database was not modified")

	case actAdoptCopy:
		return adopt(ctx, dbPath, p, st, logger)

	case actDiscardPartialCopy:
		logger.Warn("discarding a partially written compacted copy", "path", p.Copy)
		if err := os.Remove(p.Copy); err != nil {
			return fmt.Errorf("remove partial copy %s: %w", p.Copy, err)
		}
		if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
		return finishInterrupted(dbPath, st, logger,
			"a compaction was interrupted while copying; the database was not modified")

	case actDiscardOrphanCopy:
		logger.Warn("removing an orphaned compacted copy with no operation behind it", "path", p.Copy)
		if err := os.Remove(p.Copy); err != nil {
			return fmt.Errorf("remove orphaned copy %s: %w", p.Copy, err)
		}
		return SyncDir(dirOf(dbPath))

	case actKeepBothWarn:
		logger.Error("unexpected database file set: live, compacted copy and displaced original all present",
			"live", p.Live, "copy", p.Copy, "displaced_original", p.Old,
			"action", "booting on the live database, removing the copy, leaving the displaced original in place")
		if err := os.Remove(p.Copy); err != nil {
			return fmt.Errorf("remove copy %s: %w", p.Copy, err)
		}
		if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
		return finishInterrupted(dbPath, st, logger, fmt.Sprintf(
			"unexpected files beside the database; booted on the live database and left %s in place for you to inspect",
			p.Old))

	case actResumeSwap:
		logger.Warn("resuming an interrupted database swap", "copy", p.Copy, "displaced_original", p.Old)
		if !st.Phase.authorisesSwap() {
			logger.Error("the journal does not describe a swap, but the live database is missing and a copy is present",
				"phase", string(st.Phase), "action", "moving the copy into place")
		}
		return completeSwap(dbPath, p, st, logger)

	case actPromoteCopy:
		logger.Error("only a compacted copy is present; promoting it to the live database",
			"copy", p.Copy, "live", p.Live)
		return completeSwap(dbPath, p, st, logger)

	case actRollback:
		logger.Error("the compacted copy is gone and the original was already displaced; rolling back",
			"displaced_original", p.Old, "live", p.Live)
		if err := os.Rename(p.Old, p.Live); err != nil {
			return fmt.Errorf("roll back %s to %s: %w", p.Old, p.Live, err)
		}
		if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
		st.Phase = PhaseFailed
		st.Error = "the compacted copy was lost before it could be adopted; the original database was restored"
		return finish(dbPath, st, logger, LevelError, st.Error)

	case actFinishCleanup:
		return cleanupOld(dbPath, p, st, logger)

	case actAmbiguousOld:
		logger.Error("a displaced original is present but the journal cannot say whether the swap completed",
			"live", p.Live, "displaced_original", p.Old,
			"action", "booting on the live database and touching nothing")
		return finishInterrupted(dbPath, st, logger, fmt.Sprintf(
			"a previous compaction left %s behind and its outcome could not be determined; "+
				"the server booted on %s — delete the other file once you have confirmed the data is right",
			p.Old, p.Live))

	case actAllGone:
		logger.Error("no database file of any kind is present after an interrupted compaction",
			"expected", p.Live, "phase", string(st.Phase))
		st.Phase = PhaseFailed
		st.Error = "no database survived an interrupted compaction; an empty one will be created"
		return finish(dbPath, st, logger, LevelError, st.Error)
	}
	return nil
}

// adopt verifies a candidate copy and, only if it holds up, performs the swap.
func adopt(ctx context.Context, dbPath string, p Paths, st State, logger *slog.Logger) error {
	if st.Source == nil {
		// No recorded fingerprint means nothing to verify against. Refuse:
		// the live database is intact and a re-run costs a few minutes, while
		// adopting an unverifiable copy risks everything.
		logger.Error("a compacted copy is waiting but the journal records nothing to verify it against",
			"copy", p.Copy, "action", "discarding the copy and keeping the current database")
		if err := os.Remove(p.Copy); err != nil {
			return fmt.Errorf("remove unverifiable copy %s: %w", p.Copy, err)
		}
		if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
		return finishInterrupted(dbPath, st, logger,
			"a compacted copy could not be verified and was discarded; the database was not modified")
	}

	started := time.Now()
	if err := VerifyCopy(ctx, p.Copy, *st.Source); err != nil {
		logger.Error("compacted copy failed verification; keeping the current database",
			"copy", p.Copy, "err", err)
		if rerr := os.Remove(p.Copy); rerr != nil {
			return fmt.Errorf("remove unverified copy %s: %w", p.Copy, rerr)
		}
		if serr := SyncDir(dirOf(dbPath)); serr != nil {
			return serr
		}
		st.Phase = PhaseFailed
		st.Error = "the compacted copy failed verification and was discarded: " + err.Error()
		return finish(dbPath, st, logger, LevelError, st.Error)
	}
	logger.Info("compacted copy verified", "took", time.Since(started).Round(time.Millisecond).String())

	// Fold the live database's log into it so the file is self-contained
	// before it is renamed away from its sidecars.
	if err := storage.CheckpointWAL(p.Live); err != nil {
		logger.Warn("could not checkpoint the live database before swapping; continuing",
			"err", err)
	}

	// Journal the intent before the first rename: from here on the file set
	// is mid-protocol and a crash must be resumable.
	st.Phase = PhaseSwapping
	st.Message = "replacing the database with the compacted copy"
	if err := Save(dbPath, st); err != nil {
		return fmt.Errorf("journal the swap before starting it: %w", err)
	}

	if err := os.Rename(p.Live, p.Old); err != nil {
		return fmt.Errorf("displace the original database: %w", err)
	}
	if err := SyncDir(dirOf(dbPath)); err != nil {
		return err
	}
	return completeSwap(dbPath, p, st, logger)
}

// completeSwap moves the candidate into the live name and cleans up. It is the
// resume point for a crash after the original was displaced, so it must
// tolerate having already been run.
func completeSwap(dbPath string, p Paths, st State, logger *slog.Logger) error {
	// The displaced original's log files still carry the *old* name. Left in
	// place they would shadow whatever takes that name next, which is a real
	// corruption vector and the reason storage.CheckpointWAL exists.
	removeIfExists(p.Live + "-wal")
	removeIfExists(p.Live + "-shm")

	if exists(p.Copy) {
		if err := os.Rename(p.Copy, p.Live); err != nil {
			return fmt.Errorf("move the compacted copy into place: %w", err)
		}
		if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
	}
	return cleanupOld(dbPath, p, st, logger)
}

// cleanupOld records what was freed and removes the displaced original. This
// is the last step and the only one whose failure is harmless.
func cleanupOld(dbPath string, p Paths, st State, logger *slog.Logger) error {
	if oldInfo, err := os.Stat(p.Old); err == nil {
		if newInfo, nerr := os.Stat(p.Live); nerr == nil {
			if freed := oldInfo.Size() - newInfo.Size(); freed > 0 {
				st.FreedBytes = freed
			}
		}
		if err := os.Remove(p.Old); err != nil {
			// The database is already correct; a leftover original is untidy,
			// not dangerous.
			logger.Warn("could not remove the displaced original database",
				"path", p.Old, "err", err)
		} else if err := SyncDir(dirOf(dbPath)); err != nil {
			return err
		}
	}

	st.Phase = PhaseDone
	st.Error = ""
	msg := "database compaction complete"
	if st.FreedBytes > 0 {
		msg = fmt.Sprintf("database compaction complete, %d bytes reclaimed", st.FreedBytes)
	}
	st.Message = msg
	logger.Info("database compaction complete", "freed_bytes", st.FreedBytes)
	return finish(dbPath, st, logger, LevelInfo, msg)
}

// finishInterrupted records a run that did not complete but left the database
// untouched.
func finishInterrupted(dbPath string, st State, logger *slog.Logger, msg string) error {
	st.Phase = PhaseInterrupted
	st.Message = msg
	logger.Warn("previous database compaction did not finish", "detail", msg)
	return finish(dbPath, st, logger, LevelWarn, msg)
}

// finish stamps the terminal state and appends it to the trail. A journal in a
// terminal phase is kept rather than deleted: it is how the dashboard reports
// the outcome of an operation whose result could not be written to the
// database it just replaced.
func finish(dbPath string, st State, logger *slog.Logger, lvl Level, msg string) error {
	now := time.Now().UTC()
	st.FinishedAt = &now
	ev := Event{At: now, Level: lvl, Phase: st.Phase, Message: msg, RunID: st.RunID}
	st.Events = append(st.Events, ev)
	if err := AppendEvent(dbPath, ev); err != nil {
		logger.Warn("could not append to the maintenance event log", "err", err)
	}
	if err := Save(dbPath, st); err != nil {
		return fmt.Errorf("record the outcome of the compaction: %w", err)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func removeIfExists(path string) {
	if exists(path) {
		_ = os.Remove(path)
	}
}

func dirOf(dbPath string) string { return filepath.Dir(dbPath) }
