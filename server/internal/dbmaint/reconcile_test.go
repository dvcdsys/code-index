package dbmaint

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeDB creates a real database with the production schema and n users, so
// fingerprints in these tests are the same ones production computes.
func makeDB(t *testing.T, path string, users int) {
	t.Helper()
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer sdb.Close()
	for i := range users {
		if _, err := sdb.Exec(
			`INSERT INTO users (id, email, password_hash, role, created_at, updated_at)
			 VALUES (?, ?, 'x', 'user', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@example.test", i),
		); err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
	}
}

// vacuumInto produces a compacted copy the same way the compactor does.
func vacuumInto(t *testing.T, src, dst string) {
	t.Helper()
	sdb, err := db.Open(src)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`VACUUM INTO ?`, dst); err != nil {
		t.Fatalf("vacuum into %s: %v", dst, err)
	}
}

func fingerprintOf(t *testing.T, path string) Fingerprint {
	t.Helper()
	f, err := FingerprintFile(context.Background(), path)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", path, err)
	}
	return f
}

func countUsers(t *testing.T, path string) int {
	t.Helper()
	sdb, err := sql.Open(db.DriverName, "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer sdb.Close()
	var n int
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users in %s: %v", path, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// The decision table
// ---------------------------------------------------------------------------

// TestDecide_EveryCombination pins the whole reconciliation table. It is
// exhaustive on purpose: this function decides whether a database is replaced
// or left alone, and a wrong cell here is the one bug in this feature that
// destroys data rather than merely failing.
func TestDecide_EveryCombination(t *testing.T) {
	all := []Phase{
		PhaseIdle, PhasePreparing, PhaseCopying, PhaseReadyToSwap,
		PhaseSwapping, PhaseRestarting, PhaseDone, PhaseFailed, PhaseInterrupted, "",
		Phase("something-a-future-version-writes"),
	}

	// want maps a phase to the expected action for one file set. A nil entry
	// means "every phase gives this action".
	cases := []struct {
		name                     string
		hasLive, hasCopy, hasOld bool
		want                     map[Phase]action
		fallback                 action
	}{
		{
			name: "live only", hasLive: true,
			want: map[Phase]action{
				PhaseIdle: actNothing, PhaseDone: actNothing, PhaseFailed: actNothing,
				PhaseInterrupted: actNothing, "": actNothing,
			},
			fallback: actMarkInterrupted,
		},
		{
			name: "live and copy", hasLive: true, hasCopy: true,
			want: map[Phase]action{
				PhaseReadyToSwap: actAdoptCopy, PhaseSwapping: actAdoptCopy, PhaseRestarting: actAdoptCopy,
				PhasePreparing: actDiscardPartialCopy, PhaseCopying: actDiscardPartialCopy,
			},
			fallback: actDiscardOrphanCopy,
		},
		{
			name: "live, copy and displaced original", hasLive: true, hasCopy: true, hasOld: true,
			fallback: actKeepBothWarn,
		},
		{
			name: "copy and displaced original", hasCopy: true, hasOld: true,
			fallback: actResumeSwap,
		},
		{
			name: "copy only", hasCopy: true,
			fallback: actPromoteCopy,
		},
		{
			name: "displaced original only", hasOld: true,
			fallback: actRollback,
		},
		{
			name: "live and displaced original", hasLive: true, hasOld: true,
			want: map[Phase]action{
				PhaseSwapping: actFinishCleanup, PhaseDone: actFinishCleanup,
			},
			fallback: actAmbiguousOld,
		},
		{
			name: "nothing at all",
			want: map[Phase]action{
				PhaseIdle: actNothing, PhaseDone: actNothing, PhaseFailed: actNothing,
				PhaseInterrupted: actNothing, "": actNothing,
			},
			fallback: actAllGone,
		},
	}

	for _, c := range cases {
		for _, ph := range all {
			want, ok := c.want[ph]
			if !ok {
				want = c.fallback
			}
			got := decide(c.hasLive, c.hasCopy, c.hasOld, ph)
			if got != want {
				t.Errorf("decide(%s, phase=%q) = %d, want %d", c.name, ph, got, want)
			}
		}
	}
}

// A phase nobody has heard of must never authorise replacing a database.
func TestDecide_UnknownPhaseNeverAdopts(t *testing.T) {
	future := Phase("compacting-v2")
	if got := decide(true, true, false, future); got == actAdoptCopy {
		t.Error("an unrecognised phase authorised adopting a copy; unknown must mean 'do not touch the database'")
	}
}

// ---------------------------------------------------------------------------
// Real files
// ---------------------------------------------------------------------------

func TestReconcile_AdoptsVerifiedCopy(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 7)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)

	src := fingerprintOf(t, live)
	if err := Save(live, State{
		RunID: "r1", Kind: KindCompact, Phase: PhaseReadyToSwap, Source: &src,
	}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if exists(p.Copy) {
		t.Error("the copy is still present after being adopted")
	}
	if exists(p.Old) {
		t.Error("the displaced original was not cleaned up")
	}
	if got := countUsers(t, live); got != 7 {
		t.Errorf("live database has %d users after the swap, want 7", got)
	}
	st, _, err := Load(live)
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	if st.Phase != PhaseDone {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseDone)
	}
}

// The copy must be proved to be this database before the original is touched.
// A copy of some *other* database has a valid header, opens fine, and passes
// quick_check — only the fingerprint catches it.
func TestReconcile_RefusesCopyOfADifferentDatabase(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	other := filepath.Join(dir, "other.db")
	makeDB(t, live, 7)
	makeDB(t, other, 2)
	p := PathsFor(live)
	vacuumInto(t, other, p.Copy)

	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := countUsers(t, live); got != 7 {
		t.Fatalf("the wrong database was adopted: live has %d users, want the original 7", got)
	}
	if exists(p.Copy) {
		t.Error("the rejected copy was left on disk")
	}
	st, _, _ := Load(live)
	if st.Phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseFailed)
	}
}

func TestReconcile_RefusesTruncatedCopy(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 5)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)
	if err := os.Truncate(p.Copy, 100); err != nil {
		t.Fatalf("truncate copy: %v", err)
	}

	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}
	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := countUsers(t, live); got != 5 {
		t.Fatalf("live database damaged: %d users, want 5", got)
	}
}

// Without a recorded source fingerprint there is nothing to verify against,
// and an unverifiable copy must never replace a working database.
func TestReconcile_RefusesCopyWithNoRecordedFingerprint(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 3)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)

	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap}); err != nil {
		t.Fatalf("save journal: %v", err)
	}
	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := countUsers(t, live); got != 3 {
		t.Fatalf("live database changed: %d users, want 3", got)
	}
	if exists(p.Copy) {
		t.Error("the unverifiable copy was left on disk")
	}
}

// Crash between displacing the original and moving the copy into place.
func TestReconcile_ResumesInterruptedSwap(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 11)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)
	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseSwapping, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}
	// Simulate the crash: the original has been displaced, nothing else.
	if err := os.Rename(live, p.Old); err != nil {
		t.Fatalf("displace original: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !exists(live) {
		t.Fatal("no live database after resuming the swap")
	}
	if got := countUsers(t, live); got != 11 {
		t.Errorf("live database has %d users, want 11", got)
	}
	if exists(p.Old) || exists(p.Copy) {
		t.Error("leftovers after a resumed swap")
	}
}

// The copy is lost after the original was displaced: the original must come
// back, intact.
func TestReconcile_RollsBackWhenTheCopyIsLost(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 9)
	p := PathsFor(live)
	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseSwapping, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}
	if err := os.Rename(live, p.Old); err != nil {
		t.Fatalf("displace original: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := countUsers(t, live); got != 9 {
		t.Fatalf("rollback lost data: %d users, want 9", got)
	}
	st, _, _ := Load(live)
	if st.Phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseFailed)
	}
}

// A live database beside a displaced original, with nothing saying which is
// which, must be left exactly as found.
func TestReconcile_AmbiguousLeavesBothFilesAlone(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 4)
	p := PathsFor(live)
	makeDB(t, p.Old, 4)
	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !exists(live) || !exists(p.Old) {
		t.Fatal("an ambiguous file set was modified; both files must survive untouched")
	}
	st, _, _ := Load(live)
	if st.Phase != PhaseInterrupted {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseInterrupted)
	}
	if !strings.Contains(st.Message, p.Old) {
		t.Errorf("the operator was not told which file was left behind: %q", st.Message)
	}
}

func TestReconcile_DiscardsPartialCopy(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 6)
	p := PathsFor(live)
	if err := os.WriteFile(p.Copy, []byte("half a database"), 0o600); err != nil {
		t.Fatalf("write partial copy: %v", err)
	}
	if err := Save(live, State{RunID: "r1", Phase: PhaseCopying}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if exists(p.Copy) {
		t.Error("a partial copy survived reconciliation")
	}
	if got := countUsers(t, live); got != 6 {
		t.Errorf("live database has %d users, want 6", got)
	}
}

// Running the reconciler twice must be safe: a boot that crashes right after
// reconciling gets another one.
func TestReconcile_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 8)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)
	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	for i := range 3 {
		if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
			t.Fatalf("reconcile pass %d: %v", i, err)
		}
		if got := countUsers(t, live); got != 8 {
			t.Fatalf("pass %d: live database has %d users, want 8", i, got)
		}
	}
}

// A stale -wal belonging to the displaced original must not be left beside the
// file that takes its name. This is the trap storage.CheckpointWAL exists for.
func TestReconcile_RemovesStaleSidecarsOnSwap(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 5)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)
	if err := os.WriteFile(live+"-wal", []byte("stale log"), 0o600); err != nil {
		t.Fatalf("write stale wal: %v", err)
	}
	if err := os.WriteFile(live+"-shm", []byte("stale shm"), 0o600); err != nil {
		t.Fatalf("write stale shm: %v", err)
	}
	src := fingerprintOf(t, live)
	if err := Save(live, State{RunID: "r1", Phase: PhaseReadyToSwap, Source: &src}); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if exists(live + "-wal") {
		t.Error("a stale -wal survived the swap and would shadow the new database")
	}
	if exists(live + "-shm") {
		t.Error("a stale -shm survived the swap")
	}
	if got := countUsers(t, live); got != 5 {
		t.Errorf("live database has %d users, want 5", got)
	}
}

func TestReconcile_FreshInstallIsANoOp(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if exists(JournalPath(live)) {
		t.Error("a journal was written for a fresh install")
	}
}

// ---------------------------------------------------------------------------
// Journal
// ---------------------------------------------------------------------------

// A corrupt journal is evidence. It must be kept, and it must not stop the
// server: the files on disk are what authorise anything destructive.
func TestLoad_QuarantinesCorruptJournalAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	if err := os.WriteFile(JournalPath(live), []byte(`{"phase": "ready_to_`), 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}

	st, ok, err := Load(live)
	if err == nil {
		t.Error("Load did not report the corrupt journal")
	}
	if ok {
		t.Error("Load claimed to have read a corrupt journal")
	}
	if st.Phase != PhaseIdle {
		t.Errorf("phase = %q, want %q", st.Phase, PhaseIdle)
	}
	entries, _ := os.ReadDir(dir)
	var kept bool
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bad-") {
			kept = true
		}
		if e.Name() == journalName {
			t.Error("the corrupt journal is still in place")
		}
	}
	if !kept {
		t.Error("the corrupt journal was deleted instead of being kept as evidence")
	}
}

// A truncated journal must not take the database with it.
func TestReconcile_SurvivesCorruptJournalWithACopyPresent(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	makeDB(t, live, 12)
	p := PathsFor(live)
	vacuumInto(t, live, p.Copy)
	if err := os.WriteFile(JournalPath(live), []byte(`{"phase":`), 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// No readable intent means the copy is an orphan, not a candidate.
	if got := countUsers(t, live); got != 12 {
		t.Fatalf("live database changed: %d users, want 12", got)
	}
	if exists(p.Copy) {
		t.Error("the orphaned copy was left on disk")
	}
}

func TestSave_IsAtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	f := Fingerprint{Users: 3, Projects: 4, APIKeys: 5, SchemaVer: 18}
	want := State{RunID: "abc", Kind: KindCompact, Phase: PhaseCopying, Source: &f, BytesTotal: 99}
	if err := Save(live, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := Load(live)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.RunID != want.RunID || got.Phase != want.Phase || got.BytesTotal != want.BytesTotal {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Source == nil || !got.Source.Equal(f) {
		t.Errorf("fingerprint did not survive the round trip: %+v", got.Source)
	}
	// No temporary files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary journal file left behind: %s", e.Name())
		}
	}
}

func TestSave_TrimsTheEventWindow(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	st := State{RunID: "r", Phase: PhaseCopying}
	for i := range maxJournalEvents * 2 {
		st.Events = append(st.Events, Event{Message: fmt.Sprintf("event %d", i)})
	}
	if err := Save(live, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _, err := Load(live)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Events) != maxJournalEvents {
		t.Fatalf("kept %d events, want %d", len(got.Events), maxJournalEvents)
	}
	// The *most recent* events are the ones worth keeping.
	if last := got.Events[len(got.Events)-1].Message; last != fmt.Sprintf("event %d", maxJournalEvents*2-1) {
		t.Errorf("kept the wrong end of the trail: last event is %q", last)
	}
}

func TestAppendEvent_AndReadBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	for i := range 5 {
		if err := AppendEvent(live, Event{Phase: PhaseCopying, Message: fmt.Sprintf("step %d", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// A malformed line must not hide the rest of the trail.
	f, err := os.OpenFile(LogPath(live), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	f.WriteString("not json at all\n")
	f.Close()
	if err := AppendEvent(live, Event{Phase: PhaseDone, Message: "step 5"}); err != nil {
		t.Fatalf("append after garbage: %v", err)
	}

	evs, err := ReadEvents(live, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(evs) != 6 {
		t.Fatalf("read %d events, want 6 (the malformed line skipped)", len(evs))
	}
	if evs[5].Message != "step 5" {
		t.Errorf("last event = %q", evs[5].Message)
	}
	if evs[0].At.IsZero() {
		t.Error("AppendEvent did not stamp a time")
	}

	tail, err := ReadEvents(live, 2)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(tail) != 2 || tail[1].Message != "step 5" {
		t.Errorf("tail = %+v", tail)
	}
}

func TestReadEvents_MissingLogIsNotAnError(t *testing.T) {
	evs, err := ReadEvents(filepath.Join(t.TempDir(), "projects.db"), 10)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("got %d events from a missing log", len(evs))
	}
}

// The swap deletes the displaced original's -wal, and an uncheckpointed log
// holds committed transactions the file itself does not. If the checkpoint is
// allowed to fail and the swap proceeds anyway, the displaced original is
// silently short of its last writes — and that file is exactly what the
// rollback path restores when the copy is then lost. So a failed checkpoint
// aborts the whole adoption: keep the database, discard the copy.
func TestAdopt_RefusesWhenTheLogCannotBeFoldedIn(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "projects.db")
	p := PathsFor(live)

	// A real, verifiable candidate...
	src := filepath.Join(dir, "src.db")
	makeDB(t, src, 3)
	vacuumInto(t, src, p.Copy)
	fp := fingerprintOf(t, p.Copy)

	// ...and a live file whose log cannot be folded in, because it is not a
	// database at all. Standing in for the disk, permission and damaged-log
	// failures that are awkward to produce on demand and land in the same
	// branch.
	const garbage = "this is not a database\n"
	if err := os.WriteFile(live, []byte(garbage), 0o600); err != nil {
		t.Fatalf("write live: %v", err)
	}

	if err := Save(live, State{RunID: "r1", Kind: KindCompact, Phase: PhaseReadyToSwap, Source: &fp}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := Reconcile(context.Background(), live, quietLogger()); err != nil {
		t.Fatalf("reconcile returned an error instead of keeping the database: %v", err)
	}

	if exists(p.Copy) {
		t.Error("the compacted copy is still present after an adoption that could not proceed")
	}
	if exists(p.Old) {
		t.Fatal("the original was displaced even though its log could not be folded in")
	}
	got, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("read live: %v", err)
	}
	if string(got) != garbage {
		t.Error("the live database was modified by an adoption that was supposed to refuse")
	}

	st, _, err := Load(live)
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	if st.Phase != PhaseFailed {
		t.Errorf("phase = %q, want %q — a refused adoption has to be reported", st.Phase, PhaseFailed)
	}
	if st.Error == "" {
		t.Error("the journal records no reason for the refusal")
	}
}

// The public status endpoint reads this on every poll, unauthenticated. Its
// cost has to be a function of what is displayed, not of how long the server
// has been running.
func TestReadEvents_ReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")

	// Comfortably past the tail window, without writing megabytes one line at
	// a time.
	const padding = 2 << 10
	const events = (tailBytes / padding) * 3
	for i := range events {
		if err := AppendEvent(path, Event{
			Level:   LevelInfo,
			Phase:   PhaseCopying,
			Message: fmt.Sprintf("%d-%s", i, strings.Repeat("x", padding)),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// n = 0 asks for everything the reader is willing to give. It must still
	// be bounded.
	all, err := ReadEvents(path, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no events read")
	}
	if len(all) >= events {
		t.Errorf("read %d of %d events; the whole trail is being parsed on every poll", len(all), events)
	}

	// And the tail is the *newest* end of the file, not the oldest.
	last := all[len(all)-1]
	if !strings.HasPrefix(last.Message, fmt.Sprintf("%d-", events-1)) {
		t.Errorf("the last event read is %.20q…, want the most recently appended", last.Message)
	}
}

// A trail nothing ever truncates is a slow disk leak on a server whose job is
// reclaiming space.
func TestAppendEvent_RollsTheTrailOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")

	big := strings.Repeat("y", 64<<10)
	for i := 0; i < (maxLogBytes/len(big))+2; i++ {
		if err := AppendEvent(path, Event{Level: LevelInfo, Message: big}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	info, err := os.Stat(LogPath(path))
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() >= maxLogBytes {
		t.Errorf("the trail is %d bytes and still growing; it was never rolled over", info.Size())
	}
	if !exists(LogPath(path) + ".1") {
		t.Error("no previous generation was kept; the record of the last run was simply deleted")
	}
}

// The journal is read by a *different* process than the one that wrote it, so
// its wire shape has to be stable JSON, not Go-internal state.
func TestState_SerialisesAsPlainJSON(t *testing.T) {
	f := Fingerprint{Users: 1, Projects: 2, APIKeys: 3, SchemaVer: 18, PageCount: 10, PageSize: 4096}
	raw, err := json.Marshal(State{RunID: "r", Phase: PhaseReadyToSwap, Source: &f})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["phase"] != string(PhaseReadyToSwap) {
		t.Errorf("phase serialised as %v", back["phase"])
	}
	if _, ok := back["source"]; !ok {
		t.Error("the source fingerprint is missing from the journal")
	}
}
