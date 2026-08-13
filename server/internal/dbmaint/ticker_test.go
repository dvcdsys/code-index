package dbmaint

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }
func strPtr(v string) *string {
	return &v
}

// The environment layer is never validated where it is read — config.Load has
// no database to merge it against — so a compose file that half-configures a
// window has to be caught somewhere. Half a window is the dangerous one: the
// window is simply dropped, and a full compaction that was meant for 03:00
// freezes the server in the middle of the working day.
func TestValidateEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     ScheduleEnv
		wantErr bool
	}{
		{"nothing set", ScheduleEnv{}, false},
		{"a complete window", ScheduleEnv{WindowStartHour: intPtr(22), WindowEndHour: intPtr(4)}, false},
		{"only the start of a window", ScheduleEnv{WindowStartHour: intPtr(22)}, true},
		{"only the end of a window", ScheduleEnv{WindowEndHour: intPtr(4)}, true},
		{"an hour that does not exist", ScheduleEnv{WindowStartHour: intPtr(25), WindowEndHour: intPtr(4)}, true},
		{"an interval of zero", ScheduleEnv{IntervalHours: intPtr(0)}, true},
		{"a negative interval", ScheduleEnv{IntervalHours: intPtr(-1)}, true},
		{"a percentage over 100", ScheduleEnv{MinFreePercent: intPtr(150)}, true},
		{"an unknown mode", ScheduleEnv{Mode: strPtr("sideways")}, true},
		{"a usable schedule", ScheduleEnv{
			Enabled: boolPtr(true), Mode: strPtr("full"), IntervalHours: intPtr(24),
			MinFreePercent: intPtr(30), WindowStartHour: intPtr(2), WindowEndHour: intPtr(5),
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateEnv(c.env)
			if c.wantErr && err == nil {
				t.Error("accepted a schedule that cannot be honoured as written")
			}
			if !c.wantErr && err != nil {
				t.Errorf("rejected a usable schedule: %v", err)
			}
		})
	}
}

// And the scheduler acts on that: an unusable schedule is refused rather than
// half-honoured. Without the check the tick below fires, because DueNow only
// consults the window when *both* bounds are set.
func TestScheduleTick_WillNotActOnAnUnusableSchedule(t *testing.T) {
	newSvc := func(t *testing.T, env ScheduleEnv) (*Service, string) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "projects.db")
		makeDB(t, path, 1)
		sdb, err := db.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { sdb.Close() })
		return New(Deps{DB: sdb, DBPath: path, Logger: quietLogger(), Env: env}), path
	}

	// Thresholds of zero so the only thing standing between this tick and a
	// run is the validity of the schedule itself.
	base := ScheduleEnv{
		Enabled:        boolPtr(true),
		Mode:           strPtr(string(ModeIncremental)),
		MinFreePercent: intPtr(0),
		MinFreeBytes:   int64Ptr(0),
	}

	t.Run("an interval of zero does not fire", func(t *testing.T) {
		env := base
		env.IntervalHours = intPtr(0)
		svc, path := newSvc(t, env)
		svc.scheduleTickOnce(context.Background())
		if exists(JournalPath(path)) {
			t.Error("a schedule with an interval of zero ran; it would then run on every tick, forever")
		}
	})

	t.Run("half a window does not fire", func(t *testing.T) {
		env := base
		env.IntervalHours = intPtr(1)
		env.WindowStartHour = intPtr(3)
		svc, path := newSvc(t, env)
		svc.scheduleTickOnce(context.Background())
		if exists(JournalPath(path)) {
			t.Error("a schedule with only half a window ran, ignoring the window entirely")
		}
	})

	// The control: the same tick with a schedule that *is* usable does run, so
	// the two cases above are being refused rather than simply never due.
	t.Run("a usable schedule does fire", func(t *testing.T) {
		env := base
		env.IntervalHours = intPtr(1)
		svc, path := newSvc(t, env)
		svc.scheduleTickOnce(context.Background())
		if !exists(JournalPath(path)) {
			t.Fatal("a usable schedule did not run; the other two cases prove nothing")
		}
		st, _, err := Load(path)
		if err != nil {
			t.Fatalf("load journal: %v", err)
		}
		if st.Kind != KindReclaim {
			t.Errorf("journalled kind = %q, want %q", st.Kind, KindReclaim)
		}
	})
}

// The journal has one owner. A scheduled reclaim finishing while a rebuild is
// preparing must not report "reclaim done" over it — the server is frozen and
// about to restart, and a crash in that window would leave the reconciler
// believing the compaction never happened.
func TestScheduledReclaim_DoesNotOverwriteARebuildsJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.db")
	makeDB(t, path, 1)
	sdb, err := db.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	svc := New(Deps{DB: sdb, DBPath: path, Logger: quietLogger()})

	// What a rebuild leaves behind before it quiesces.
	rebuild := State{RunID: "rebuild", Kind: KindCompact, Phase: PhasePreparing}
	if err := Save(path, rebuild); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	svc.mu.Lock()
	svc.running = true
	svc.mu.Unlock()

	svc.runScheduledReclaim(context.Background())

	st, _, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.RunID != "rebuild" || st.Phase != PhasePreparing {
		t.Errorf("the journal now reads run=%q phase=%q; a reclaim overwrote a rebuild in progress",
			st.RunID, st.Phase)
	}
}

func int64Ptr(v int64) *int64 { return &v }
