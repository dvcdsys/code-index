package schedule

import (
	"testing"
	"time"
)

func kyiv(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Skipf("no tzdata for Europe/Kyiv: %v", err)
	}
	return loc
}

func at(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

// The expressions the underlying library gets wrong, pinned.
//
// Measured on gronx v1.20.1: NextTickAfter("0 0 31 * *", 2026-08-31) answers
// 2026-10-02, and NextTickAfter("0 0 29 2 *", …) answers 2027-03-04 — dates
// that do not satisfy their own expression. If a future version fixes it these
// still pass; if a future version breaks something else the same way, they
// fail. Either way the server never fires on a day crontab would not.
func TestNextAfter_RejectsCandidatesThatDoNotMatchTheirOwnExpression(t *testing.T) {
	loc := kyiv(t)
	cases := []struct {
		name string
		expr string
		from time.Time
		want []time.Time
	}{
		{
			name: "the 31st skips the months that have none",
			expr: "0 0 31 * *",
			from: at(loc, 2026, time.August, 13, 12, 0),
			want: []time.Time{
				at(loc, 2026, time.August, 31, 0, 0),
				at(loc, 2026, time.October, 31, 0, 0),
				at(loc, 2026, time.December, 31, 0, 0),
				at(loc, 2027, time.January, 31, 0, 0),
			},
		},
		{
			name: "29 February happens only in leap years",
			expr: "0 0 29 2 *",
			from: at(loc, 2026, time.August, 13, 12, 0),
			want: []time.Time{
				at(loc, 2028, time.February, 29, 0, 0),
				at(loc, 2032, time.February, 29, 0, 0),
				at(loc, 2036, time.February, 29, 0, 0),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextRuns(c.expr, c.from, len(c.want))
			if err != nil {
				t.Fatalf("next runs: %v", err)
			}
			for i, w := range c.want {
				if !got[i].Equal(w) {
					t.Errorf("run %d = %s, want %s", i+1,
						got[i].Format(time.RFC3339), w.Format(time.RFC3339))
				}
			}
		})
	}
}

// An expression that parses but can never match is a configuration error, not
// a schedule. Accepting it would leave an admin with automation that silently
// never runs and no way to tell that from one that is merely not due yet.
func TestValidate_RefusesAnExpressionThatCanNeverRun(t *testing.T) {
	if err := Validate("0 0 30 2 *"); err == nil {
		t.Error("30 February was accepted as a schedule")
	}
}

func TestValidate(t *testing.T) {
	ok := []string{"0 0 * * *", "*/15 * * * *", "0 3 * * 0", "@daily", "0 */6 * * *", "0 0 1 * *"}
	for _, e := range ok {
		if err := Validate(e); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", e, err)
		}
	}
	bad := []string{"", "* * *", "99 * * * *", "0 0 * * 8", "every day please"}
	for _, e := range bad {
		if err := Validate(e); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", e)
		}
	}
}

// The next run is a function of the clock and the expression, never of when
// the last run happened to finish. This is what keeps a schedule from drifting:
// a job that starts at 00:00 and takes four minutes is still due at 00:00
// tomorrow, not 00:04.
func TestNextAfter_IsAnchoredToTheClockNotToCompletion(t *testing.T) {
	loc := kyiv(t)
	slot := at(loc, 2026, time.August, 14, 0, 0)
	finished := slot.Add(3*time.Minute + 42*time.Second)

	fromSlot, err := NextAfter("0 0 * * *", slot)
	if err != nil {
		t.Fatalf("from slot: %v", err)
	}
	fromFinish, err := NextAfter("0 0 * * *", finished)
	if err != nil {
		t.Fatalf("from finish: %v", err)
	}
	want := at(loc, 2026, time.August, 15, 0, 0)
	if !fromSlot.Equal(want) || !fromFinish.Equal(want) {
		t.Errorf("next from slot = %s, from completion = %s, want both %s — a schedule that "+
			"tracks completion drifts a little further every day",
			fromSlot.Format(time.RFC3339), fromFinish.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A run that overruns its own slot loses the slots it ran through. It does not
// queue them up and fire a burst afterwards, which for a task that freezes the
// server would be the worst possible behaviour.
func TestNextAfter_OverrunSkipsRatherThanQueues(t *testing.T) {
	loc := kyiv(t)
	finished := at(loc, 2026, time.August, 14, 2, 30) // started 01:00, ran 90 minutes
	next, err := NextAfter("0 * * * *", finished)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := at(loc, 2026, time.August, 14, 3, 0); !next.Equal(want) {
		t.Errorf("next = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// The hour that does not happen.
//
// Kyiv springs forward on the last Sunday of March: 03:00 becomes 04:00, so
// nothing in [03:00, 04:00) occurs that day. Pure wall-clock arithmetic finds
// no match and moves to tomorrow — a nightly task quietly missing a night once
// a year, with nothing in the log, and CatchUp powerless because no slot was
// ever armed to be late for. vixie cron runs those jobs right after the jump,
// and so does this.
func TestNextAfter_FiresAfterTheJumpForAnHourThatDoesNotExist(t *testing.T) {
	loc := kyiv(t)
	// 2027-03-28 in Kyiv: 03:00 EET -> 04:00 EEST.
	for _, expr := range []string{"30 3 * * *", "0 3 * * *", "59 3 * * *"} {
		next, err := NextAfter(expr, at(loc, 2027, time.March, 27, 12, 0))
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		want := at(loc, 2027, time.March, 28, 4, 0) // the first valid instant
		if !next.Equal(want) {
			t.Errorf("%s -> %s, want %s — the run inside the vanished hour must not be lost",
				expr, next.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}

	// And it happens once, not once per skipped minute.
	first, err := NextAfter("30 3 * * *", at(loc, 2027, time.March, 27, 12, 0))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := NextAfter("30 3 * * *", first)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if want := at(loc, 2027, time.March, 29, 3, 30); !second.Equal(want) {
		t.Errorf("the run after it = %s, want %s", second.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// An hour on either side of the gap is ordinary and must not be touched.
func TestNextAfter_LeavesTheHoursAroundTheGapAlone(t *testing.T) {
	loc := kyiv(t)
	from := at(loc, 2027, time.March, 27, 12, 0)
	for _, c := range []struct {
		expr string
		want time.Time
	}{
		{"30 2 * * *", at(loc, 2027, time.March, 28, 2, 30)},
		{"30 4 * * *", at(loc, 2027, time.March, 28, 4, 30)},
	} {
		next, err := NextAfter(c.expr, from)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if !next.Equal(c.want) {
			t.Errorf("%s -> %s, want %s", c.expr, next.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}

// And on the autumn repeat, an hour that happens twice fires once.
func TestNextAfter_FiresOnceInAnHourThatHappensTwice(t *testing.T) {
	loc := kyiv(t)
	first, err := NextAfter("30 3 * * *", at(loc, 2026, time.October, 24, 12, 0))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := NextAfter("30 3 * * *", first)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Sub(first) < 20*time.Hour {
		t.Errorf("ran twice inside the repeated hour: %s then %s",
			first.Format(time.RFC3339), second.Format(time.RFC3339))
	}
}

// Day-of-month and day-of-week are OR, not AND — the one piece of crontab
// semantics everybody implements backwards at least once.
func TestNextAfter_DayOfMonthOrDayOfWeek(t *testing.T) {
	loc := kyiv(t)
	// The 1st of the month, or any Monday.
	got, err := NextRuns("0 0 1 * 1", at(loc, 2026, time.August, 13, 12, 0), 4)
	if err != nil {
		t.Fatalf("next runs: %v", err)
	}
	want := []time.Time{
		at(loc, 2026, time.August, 17, 0, 0),   // Monday
		at(loc, 2026, time.August, 24, 0, 0),   // Monday
		at(loc, 2026, time.August, 31, 0, 0),   // Monday
		at(loc, 2026, time.September, 1, 0, 0), // the 1st, a Tuesday
	}
	for i, w := range want {
		if !got[i].Equal(w) {
			t.Errorf("run %d = %s, want %s", i+1, got[i].Format(time.RFC3339), w.Format(time.RFC3339))
		}
	}
}
