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

	if _, ok := c.take("other"); ok {
		t.Error("a different id must not resolve")
	}
	if _, ok := c.take("abc"); !ok {
		t.Fatal("a fresh analysis should be redeemable")
	}
	// take() consumes: the same id must not work twice, which is what stops
	// two concurrent cleans both reporting the same reclaimed bytes.
	if _, ok := c.take("abc"); ok {
		t.Error("an analysis must be redeemable only once")
	}

	c.put(&Analysis{ID: "def", ExpiresAt: now.Add(analysisTTL)})
	now = now.Add(analysisTTL)
	if _, ok := c.take("def"); ok {
		t.Error("an analysis exactly at its expiry must not be redeemable")
	}

	c.put(&Analysis{ID: "ghi", ExpiresAt: now.Add(analysisTTL)})
	c.invalidate()
	if _, ok := c.take("ghi"); ok {
		t.Error("an invalidated analysis must not be redeemable")
	}
}

// A follower that arrives while a scan is running must never be handed a
// leftover analysis from an earlier round — it would get a 200 with a stale
// picture whose id then 409s on clean.
func TestAnalyze_FollowerRejectsStaleLeftovers(t *testing.T) {
	f := newFixture(t)
	stale := &Analysis{ID: "stale", ExpiresAt: f.now.Add(analysisTTL)}
	f.svc.cache.put(stale)

	// Pretend a scan is in flight and then finishes without producing
	// anything — exactly the "leader failed" path.
	done, wait := f.svc.cache.beginScan()
	if done == nil {
		t.Fatal("expected to become the scan leader")
	}
	go func() { <-wait }()
	done()

	a, err := f.svc.Analyze(context.Background())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if a.ID == stale.ID {
		t.Error("the follower adopted the leftover analysis instead of scanning")
	}
}
