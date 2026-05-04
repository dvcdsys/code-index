package httpapi

import (
	"testing"
	"time"
)

// fakeClock returns a closure satisfying loginLimiter.now. Mutating *t between
// calls advances time deterministically.
func fakeClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestLoginLimiter_PerEmailWindow(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = fakeClock(&now)
	l.keyLimit = 3
	l.keyWindow = time.Minute

	for i := range 3 {
		if ok, _ := l.allow("1.2.3.4", "alice@example.com"); !ok {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}
	// Fourth in the same window must block.
	ok, retry := l.allow("1.2.3.4", "alice@example.com")
	if ok {
		t.Fatalf("expected 4th attempt to be blocked")
	}
	if retry < time.Second || retry > time.Minute {
		t.Errorf("retry = %v, want between 1s and 1m", retry)
	}
	// Different email from the same IP must still pass — per-email window
	// is keyed independently. (Per-IP cap is left at default 60/min so it
	// does not interfere here.)
	if ok, _ := l.allow("1.2.3.4", "bob@example.com"); !ok {
		t.Errorf("different email from same IP should pass")
	}
	// After the window slides past, the original counter resets.
	now = now.Add(time.Minute + time.Second)
	if ok, _ := l.allow("1.2.3.4", "alice@example.com"); !ok {
		t.Errorf("after window expiry, attempt should pass")
	}
}

func TestLoginLimiter_Reset(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = fakeClock(&now)
	l.keyLimit = 2
	l.keyWindow = time.Minute

	for range 2 {
		_, _ = l.allow("1.2.3.4", "alice@example.com")
	}
	// On the boundary now: a 3rd attempt would block.
	if ok, _ := l.allow("1.2.3.4", "alice@example.com"); ok {
		t.Fatalf("expected 3rd attempt to block before reset")
	}
	// Successful login → reset → next attempt admitted.
	l.reset("1.2.3.4", "alice@example.com")
	if ok, _ := l.allow("1.2.3.4", "alice@example.com"); !ok {
		t.Errorf("post-reset attempt should be admitted")
	}
}

func TestLoginLimiter_PerIPCap(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = fakeClock(&now)
	l.ipLimit = 3
	l.ipWindow = time.Minute
	l.keyLimit = 100 // lift the per-email cap so we exercise per-IP only

	// Three attempts across different emails — all admitted.
	for i, email := range []string{"a@x", "b@x", "c@x"} {
		if ok, _ := l.allow("1.2.3.4", email); !ok {
			t.Fatalf("attempt %d (%s) unexpectedly blocked", i+1, email)
		}
	}
	// Fourth from same IP, different email — blocked by per-IP cap.
	if ok, _ := l.allow("1.2.3.4", "d@x"); ok {
		t.Errorf("4th attempt from same IP should hit per-IP cap")
	}
	// A different IP is unaffected.
	if ok, _ := l.allow("5.6.7.8", "a@x"); !ok {
		t.Errorf("different IP should not be blocked")
	}
}

func TestLoginLimiter_EmailCaseInsensitive(t *testing.T) {
	l := newLoginLimiter()
	l.keyLimit = 1
	l.keyWindow = time.Minute

	if ok, _ := l.allow("1.2.3.4", "Alice@example.com"); !ok {
		t.Fatalf("first attempt unexpectedly blocked")
	}
	// Same email, different case — must hit the same bucket.
	if ok, _ := l.allow("1.2.3.4", "alice@EXAMPLE.com"); ok {
		t.Errorf("case-different email should share the per-email counter")
	}
}

func TestPruneOlder(t *testing.T) {
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	ts := []time.Time{
		base.Add(-3 * time.Minute),
		base.Add(-2 * time.Minute),
		base.Add(-30 * time.Second),
		base,
	}
	got := pruneOlder(ts, base.Add(-time.Minute))
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (kept entries within last minute)", len(got))
	}
}
