package versioncheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub spins up an httptest.Server that mimics the
// /repos/{owner}/{repo}/releases endpoint. The handler closure can
// flip behavior between calls (e.g. to return 304 the second time).
func fakeGitHub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func mustEncode(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func newTestService(t *testing.T, baseURL, current string) *Service {
	t.Helper()
	return New(Config{
		Enabled:        true,
		Interval:       0, // one-shot
		InitialDelay:   0,
		BaseURL:        baseURL,
		Repo:           "owner/repo",
		TagPrefix:      "server/v",
		CurrentVersion: current,
		HTTPTimeout:    2 * time.Second,
		UserAgent:      "cix-server-test",
	}, nil)
}

func TestPicksLatestServerTagSkippingCli(t *testing.T) {
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		mustEncode(t, w, []githubRelease{
			{TagName: "cli/v0.5.0", HTMLURL: "u-cli", Prerelease: false},
			{TagName: "server/v0.4.0", HTMLURL: "u-server-040", Prerelease: false},
			{TagName: "server/v0.5.1", HTMLURL: "u-server-051", Prerelease: false},
			{TagName: "server/v0.5.0", HTMLURL: "u-server-050", Prerelease: false},
		})
	})

	s := newTestService(t, srv.URL, "0.5.0")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	snap := s.Latest()
	if snap.LatestVersion != "0.5.1" {
		t.Errorf("LatestVersion = %q, want 0.5.1", snap.LatestVersion)
	}
	if !snap.UpdateAvailable {
		t.Errorf("UpdateAvailable = false, want true (current=0.5.0, latest=0.5.1)")
	}
	if snap.ReleaseURL != "u-server-051" {
		t.Errorf("ReleaseURL = %q, want u-server-051", snap.ReleaseURL)
	}
	if snap.LastError != "" {
		t.Errorf("LastError = %q, want empty", snap.LastError)
	}
}

func TestSkipsPrereleaseAndDraft(t *testing.T) {
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		mustEncode(t, w, []githubRelease{
			{TagName: "server/v0.6.0", Prerelease: true, HTMLURL: "u-pre"},
			{TagName: "server/v0.5.9", Draft: true, HTMLURL: "u-draft"},
			{TagName: "server/v0.5.1", HTMLURL: "u-good"},
		})
	})

	s := newTestService(t, srv.URL, "0.5.0")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	snap := s.Latest()
	if snap.LatestVersion != "0.5.1" {
		t.Errorf("LatestVersion = %q, want 0.5.1 (prereleases/drafts must be skipped)", snap.LatestVersion)
	}
}

func TestNoUpdateWhenCurrentIsLatest(t *testing.T) {
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		mustEncode(t, w, []githubRelease{
			{TagName: "server/v0.5.1", HTMLURL: "u-051"},
		})
	})

	s := newTestService(t, srv.URL, "0.5.1")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	snap := s.Latest()
	if snap.LatestVersion != "0.5.1" {
		t.Errorf("LatestVersion = %q, want 0.5.1", snap.LatestVersion)
	}
	if snap.UpdateAvailable {
		t.Errorf("UpdateAvailable = true, want false (running latest)")
	}
}

func TestETagRevalidation(t *testing.T) {
	var calls atomic.Int32
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// On first call, return data + ETag. On second call, expect
		// If-None-Match and respond 304.
		if n == 1 {
			w.Header().Set("ETag", `"abc123"`)
			mustEncode(t, w, []githubRelease{
				{TagName: "server/v0.6.0", HTMLURL: "u-060"},
			})
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"abc123"` {
			t.Errorf("expected If-None-Match=abc123 on second call, got %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	})

	s := newTestService(t, srv.URL, "0.5.0")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("first CheckNow: %v", err)
	}
	first := s.Latest()
	if first.LatestVersion != "0.6.0" {
		t.Fatalf("first.LatestVersion = %q, want 0.6.0", first.LatestVersion)
	}
	firstChecked := first.CheckedAt
	time.Sleep(2 * time.Millisecond) // ensure CheckedAt advances

	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("second CheckNow: %v", err)
	}
	second := s.Latest()
	if second.LatestVersion != "0.6.0" {
		t.Errorf("second.LatestVersion = %q, want 0.6.0 (304 must preserve cache)", second.LatestVersion)
	}
	if !second.CheckedAt.After(firstChecked) {
		t.Errorf("CheckedAt did not advance on 304: first=%v second=%v", firstChecked, second.CheckedAt)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 GitHub calls, got %d", calls.Load())
	}
}

func TestServerErrorPreservesCache(t *testing.T) {
	var calls atomic.Int32
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			mustEncode(t, w, []githubRelease{
				{TagName: "server/v0.5.1", HTMLURL: "u-051"},
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	s := newTestService(t, srv.URL, "0.5.0")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("first CheckNow: %v", err)
	}
	if err := s.CheckNow(context.Background()); err == nil {
		t.Fatalf("second CheckNow: expected error, got nil")
	}
	snap := s.Latest()
	if snap.LatestVersion != "0.5.1" {
		t.Errorf("LatestVersion = %q, want 0.5.1 (cache must survive 5xx)", snap.LatestVersion)
	}
	if snap.LastError == "" {
		t.Errorf("LastError empty, want populated on 5xx")
	}
}

func TestDisabledServiceDoesNotCallGitHub(t *testing.T) {
	var calls atomic.Int32
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mustEncode(t, w, []githubRelease{})
	})

	s := New(Config{
		Enabled:        false,
		Interval:       10 * time.Millisecond,
		BaseURL:        srv.URL,
		Repo:           "owner/repo",
		CurrentVersion: "0.5.0",
		HTTPTimeout:    2 * time.Second,
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx) // returns ~immediately

	if calls.Load() != 0 {
		t.Errorf("disabled service made %d GitHub calls, want 0", calls.Load())
	}
	if s.Latest().Enabled {
		t.Errorf("Latest().Enabled = true, want false")
	}
}

func TestDevBuildAlwaysSeesUpdate(t *testing.T) {
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		mustEncode(t, w, []githubRelease{
			{TagName: "server/v0.1.0", HTMLURL: "u-010"},
		})
	})

	s := newTestService(t, srv.URL, "0.0.0-dev")
	if err := s.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	if !s.Latest().UpdateAvailable {
		t.Errorf("UpdateAvailable = false, want true on dev build")
	}
}

func TestCompareSemverEdgeCases(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.5.0", "0.5.0", 0},
		{"0.5.1", "0.5.0", 1},
		{"0.5.0", "0.5.1", -1},
		{"0.10.0", "0.9.0", 1}, // numeric, not lexicographic
		{"1.0.0", "0.99.99", 1},
		{"0.5", "0.5.0", 0}, // missing component treated as 0
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		if got != c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestRunBacksOffOnFailureThenResets exercises the full Run() loop:
// the first two GitHub responses fail with 502, the third succeeds.
// Verifies the loop eventually recovers (latest version flows into
// the snapshot) without burning the whole budget on tight retries.
func TestRunBacksOffOnFailureThenResets(t *testing.T) {
	var calls atomic.Int32
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		mustEncode(t, w, []githubRelease{
			{TagName: "server/v0.6.0", HTMLURL: "u-060"},
		})
	})

	s := New(Config{
		Enabled:        true,
		Interval:       50 * time.Millisecond,
		BackoffInitial: 5 * time.Millisecond,
		BackoffMax:     20 * time.Millisecond,
		BaseURL:        srv.URL,
		Repo:           "owner/repo",
		CurrentVersion: "0.5.0",
		HTTPTimeout:    100 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	deadline := time.After(1 * time.Second)
	for s.Latest().LatestVersion != "0.6.0" {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("did not recover from failure streak; calls=%d snapshot=%+v",
				calls.Load(), s.Latest())
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	<-done

	if got := calls.Load(); got < 3 {
		t.Errorf("expected at least 3 GitHub calls (2 fails + success), got %d", got)
	}
	if got := s.Latest().LastError; got != "" {
		t.Errorf("LastError after recovery = %q, want empty (success must clear)", got)
	}
}

// TestRunStreakExhaustedAnchorsToInterval verifies that after
// MaxBackoffAttempts consecutive failures, the loop stops retrying
// on the backoff schedule and instead waits until streakStart+Interval
// (the regular schedule, anchored to the FIRST attempt of the streak).
func TestRunStreakExhaustedAnchorsToInterval(t *testing.T) {
	var calls atomic.Int32
	srv := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})

	// MaxBackoffAttempts=3, BackoffInitial=2ms, BackoffMax=8ms, Interval=150ms.
	// Expected timeline:
	//   T=0     #1 fail, wait ~2ms
	//   T=+2    #2 fail, wait ~4ms
	//   T=+6    #3 fail, EXHAUSTED → anchor wait = 150 - 6 = 144ms
	//   T=+150  #4 fail (new streak)
	s := New(Config{
		Enabled:            true,
		Interval:           150 * time.Millisecond,
		BackoffInitial:     2 * time.Millisecond,
		BackoffMax:         8 * time.Millisecond,
		MaxBackoffAttempts: 3,
		BaseURL:            srv.URL,
		Repo:               "owner/repo",
		CurrentVersion:     "0.5.0",
		HTTPTimeout:        100 * time.Millisecond,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	// After streak exhaustion the loop must be sleeping until Interval.
	// Sample halfway through the anchor wait — call count should still be
	// at exactly MaxBackoffAttempts.
	time.Sleep(80 * time.Millisecond)
	mid := calls.Load()
	if mid != 3 {
		cancel()
		<-done
		t.Fatalf("after 80ms expected exactly 3 attempts (streak exhausted, anchored sleep), got %d", mid)
	}

	// After Interval elapses since streakStart, the next attempt fires.
	time.Sleep(120 * time.Millisecond) // total ~200ms > 150ms anchor
	end := calls.Load()
	if end < 4 {
		cancel()
		<-done
		t.Fatalf("after 200ms expected at least 4 attempts (anchored retry fired), got %d", end)
	}

	cancel()
	<-done
}

func TestNetworkErrorPopulatesLastError(t *testing.T) {
	// Point at an unroutable address; the request must fail before timeout.
	s := New(Config{
		Enabled:        true,
		BaseURL:        "http://127.0.0.1:1", // closed port
		Repo:           "owner/repo",
		CurrentVersion: "0.5.0",
		HTTPTimeout:    250 * time.Millisecond,
	}, nil)
	_ = s.CheckNow(context.Background())
	if s.Latest().LastError == "" {
		t.Errorf("LastError empty, want populated on network failure")
	}
}
