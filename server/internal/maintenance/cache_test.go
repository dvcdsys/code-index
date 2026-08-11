package maintenance

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Two dashboards, a double-click, or React StrictMode's double effect all
// produce concurrent Analyze calls. Each one walks the whole vector store, so
// they must collapse into a single scan.
func TestAnalyze_SingleFlight(t *testing.T) {
	f := newFixture(t)
	f.index(t, "/dead/project")

	var mu sync.Mutex
	scans := 0
	release := make(chan struct{})
	// Count scans by observing a dependency every scan calls exactly once
	// (the model-cache lookup, used only by scanUnusedModels), and hold the
	// leader inside it until every caller has arrived.
	cacheDir := f.cfg.GGUFCacheDir
	f.svc.d.ActiveGGUFCacheDir = func() string {
		mu.Lock()
		scans++
		first := scans == 1
		mu.Unlock()
		if first {
			<-release
		}
		return cacheDir
	}

	const callers = 8
	var wg sync.WaitGroup
	ids := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := f.svc.Analyze(context.Background())
			if err != nil {
				t.Errorf("analyze: %v", err)
				return
			}
			ids[i] = a.ID
		}()
	}
	// Give the followers time to queue behind the leader, then let it finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	got := scans
	mu.Unlock()
	if got != 1 {
		t.Errorf("the scan ran %d times, want 1 — concurrent Analyze calls must collapse", got)
	}
	for i, id := range ids {
		if id == "" || id != ids[0] {
			t.Errorf("caller %d got analysis id %q, want the shared %q", i, id, ids[0])
		}
	}
}

func TestAnalysisCache_ExpiryAndInvalidate(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newAnalysisCache(analysisTTL, func() time.Time { return now })
	a := &Analysis{ID: "abc", ExpiresAt: now.Add(analysisTTL)}
	c.put(a)

	if _, ok := c.get("abc"); !ok {
		t.Fatal("a fresh analysis should be redeemable")
	}
	if _, ok := c.get("other"); ok {
		t.Error("a different id must not resolve")
	}

	now = now.Add(analysisTTL)
	if _, ok := c.get("abc"); ok {
		t.Error("an analysis exactly at its expiry must not be redeemable")
	}

	now = now.Add(-analysisTTL)
	if _, ok := c.get("abc"); !ok {
		t.Fatal("precondition: the analysis should be valid again")
	}
	c.invalidate()
	if _, ok := c.get("abc"); ok {
		t.Error("an invalidated analysis must not be redeemable")
	}
}
