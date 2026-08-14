// Package schedule runs named tasks on a crontab expression.
//
// It is deliberately not a job queue. This server already has one — the `jobs`
// table, with retries, dedupe and a worker — and a second persistence model
// beside it would mean two places to look when something did not run. What was
// missing is only the answer to "when next", so that is all this owns: a row
// per task saying when it is due, one goroutine watching the clock, and a
// handler called in-process when the time comes. A task that wants durable,
// retryable work enqueues it into `jobs`; that is the seam between the two.
//
// One recurring job is still outside it: internal/versioncheck keeps its own
// ticker. Its period is CIX_VERSION_CHECK_INTERVAL, a released, duration-valued
// variable, and a duration does not survive the trip through crontab — 6h maps
// cleanly, 7h does not exist. Moving it means either breaking that variable or
// carrying both forms, which is a decision of its own rather than a tidy-up.
//
// Database compaction is the reason it cannot simply live in the job queue:
// compaction drains that queue as part of taking the server read-only, so a
// trigger inside it would be draining itself.
package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"
)

// horizon bounds the search for a next run. An expression that matches nothing
// inside it is treated as one that never runs, which is a configuration error
// worth refusing rather than a schedule worth keeping.
//
// Ten years covers the sparsest expression anybody sensibly writes: 29 February
// recurs every four, and the leap-century rule at worst pushes that to eight.
const horizon = 10 * 365 * 24 * time.Hour

// maxProbes bounds the verification loop below. Each probe advances at least
// one matching-ish instant, so this is generous for any real expression.
const maxProbes = 512

// maxTransitions bounds the walk over clock changes in skippedByClockChange.
// Two a year in every zone that has them, against a ten-year horizon.
//
// The walk only reaches its far end for an expression whose next run is years
// away, and only such an expression pays for the search. Measured for three
// projected runs, from June, in Europe/Kyiv: 33 µs for a nightly expression,
// 153 µs for a yearly one, 7.2 ms for a yearly one carrying seconds — the last
// being both the worst case and one nobody writes.
const maxTransitions = 64

var gron = gronx.New()

// Validate reports whether expr is a usable crontab expression.
//
// "Usable" is stronger than "parses": an expression that parses but can never
// match — 30 February being the honest example — is refused, because accepting
// it would leave an admin with a schedule that silently never fires.
func Validate(expr string) error {
	if expr == "" {
		return fmt.Errorf("a schedule needs a crontab expression")
	}
	if !gronx.IsValid(expr) {
		return fmt.Errorf("%q is not a valid crontab expression", expr)
	}
	if _, err := NextAfter(expr, time.Now()); err != nil {
		return err
	}
	return nil
}

// NextAfter returns the first time strictly after `after` at which expr is due.
//
// The candidate the library returns is checked against the library's own due
// test before being believed, and stepped over when the two disagree. That is
// not defensive programming for its own sake — it is fixing a measured defect:
//
//	NextTickAfter("0 0 31 * *", 2026-08-31)  ->  2026-10-02
//	NextTickAfter("0 0 29 2 *", 2026-08-13)  ->  2027-03-04
//
// Neither answer satisfies its own expression (October 2nd is not the 31st;
// March is not February), and both look like a date overflow — "31 September"
// normalised into October — rather than the month being skipped. IsDue answers
// correctly for the same instants, so it is the half worth trusting. The other
// cron library evaluated has the same class of defect on 29 February, so this
// is a property of the ecosystem rather than of one dependency, and the check
// stays regardless of which one is underneath.
func NextAfter(expr string, after time.Time) (time.Time, error) {
	if !gronx.IsValid(expr) {
		return time.Time{}, fmt.Errorf("%q is not a valid crontab expression", expr)
	}
	deadline := after.Add(horizon)
	cursor := after
	for range maxProbes {
		cand, err := gronx.NextTickAfter(expr, cursor, false)
		if err != nil {
			return time.Time{}, fmt.Errorf("next run for %q: %w", expr, err)
		}
		if !cand.After(cursor) {
			// No forward progress. Stop rather than spin.
			return time.Time{}, fmt.Errorf("%q does not advance past %s", expr, cursor.Format(time.RFC3339))
		}
		if cand.After(deadline) {
			break
		}
		due, err := gron.IsDue(expr, cand)
		if err != nil {
			return time.Time{}, fmt.Errorf("check %q: %w", expr, err)
		}
		if due {
			if gap, ok := skippedByClockChange(expr, after, cand); ok {
				return gap, nil
			}
			return cand, nil
		}
		cursor = cand
	}
	return time.Time{}, fmt.Errorf("%q has no run within the next ten years", expr)
}

// skippedByClockChange reports a run that the spring-forward would otherwise
// swallow, and the instant to run it at instead.
//
// On the morning the clocks go forward an hour of wall time does not happen:
// in Kyiv 03:00 becomes 04:00, so `0 3 * * *` matches no instant that day and
// pure wall-clock arithmetic simply moves to tomorrow. That is a nightly task
// silently missing a night, once a year, with nothing in the log — and CatchUp
// cannot rescue it, because no slot was ever armed to be late for.
//
// vixie cron runs those jobs once, immediately after the jump, and so does
// this: if the expression matches any minute inside the vanished hour, the run
// happens at the first instant the clock is valid again.
//
// Every transition between the two times is considered, not just the first.
// ZoneBounds only ever answers about the interval containing the instant it is
// asked about, and for a sparse expression the interesting jump is rarely the
// nearest one: `0 3 28 3 *` asked in June meets the autumn change first, and
// stopping there would lose the whole of the following March.
func skippedByClockChange(expr string, after, next time.Time) (time.Time, bool) {
	loc := after.Location()
	step := probeStep(expr)
	cursor := after
	for range maxTransitions {
		_, end := cursor.ZoneBounds()
		if end.IsZero() || !end.After(cursor) || !end.Before(next) {
			// No further change before the candidate we already have.
			return time.Time{}, false
		}
		_, before := end.Add(-time.Second).Zone()
		_, atEnd := end.Zone()
		// A negative delta is the clock falling back, where nothing vanishes.
		if delta := time.Duration(atEnd-before) * time.Second; delta > 0 {
			// The wall clock jumps from (W - delta) to W at this instant, so
			// every moment in between is a time that did not occur.
			//
			// The arithmetic is on the calendar fields, not on the instant: an
			// hour before the transition *as an instant* is still an hour of
			// real time and lands at 02:00, whereas the hour that vanished is
			// 03:00–04:00. UTC is the carrier precisely because it has no
			// transitions to normalise against — and the due check reads the
			// fields, never the offset.
			w := end.In(loc)
			last := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)
			for f := last.Add(-delta); f.Before(last); f = f.Add(step) {
				if due, err := gron.IsDue(expr, f); err == nil && due {
					return end, true
				}
			}
		}
		cursor = end
	}
	return time.Time{}, false
}

// probeStep is how finely the vanished hour above has to be searched.
//
// gronx takes an optional leading seconds field, and Segments normalises every
// expression to carry one, so a literal "0" there means the expression can only
// match on the minute and a minute-wide step covers it. Anything else — "30",
// "*/10" — has to be walked a second at a time, or the rescue steps straight
// over the very schedule it exists for and loses the day after all.
func probeStep(expr string) time.Duration {
	segs, err := gronx.Segments(expr)
	if err != nil || len(segs) == 0 || segs[0] == "0" {
		return time.Minute
	}
	return time.Second
}

// NextRuns returns the next n runs after `after`.
//
// The API hands these to the dashboard so the preview beside the field is
// produced by the same code that will actually fire, rather than by a second
// cron implementation in the browser that can disagree with it.
func NextRuns(expr string, after time.Time, n int) ([]time.Time, error) {
	out := make([]time.Time, 0, n)
	cursor := after
	for range n {
		next, err := NextAfter(expr, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, next)
		cursor = next
	}
	return out, nil
}
