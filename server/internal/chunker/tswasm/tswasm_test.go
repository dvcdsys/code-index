package tswasm

import (
	"strings"
	"sync"
	"testing"
)

func TestParseNodes_Go(t *testing.T) {
	nodes, err := ParseNodes("tree_sitter_go", []byte("package m\nfunc Hello() int { return 1 }\ntype T struct{ X int }\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"function_declaration": false, "type_declaration": false}
	for _, n := range nodes {
		if _, ok := want[n.Kind]; ok {
			want[n.Kind] = true
		}
		if n.Error || n.Missing {
			t.Errorf("unexpected error/missing node %q", n.Kind)
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("kind %q not found", k)
		}
	}
}

func TestHasLanguage(t *testing.T) {
	if !HasLanguage("tree_sitter_python") {
		t.Error("python should be present")
	}
	if HasLanguage("tree_sitter_nonexistent") {
		t.Error("nonexistent grammar should be absent")
	}
}

// TestPool_RecycleAndStats verifies the high-water-mark recycling and the
// observability snapshot. With RecycleBytes forced below an instance's static
// memory floor, every release recycles (closes) the instance instead of pooling
// it — so memory never accumulates and the counters move.
func TestPool_RecycleAndStats(t *testing.T) {
	old := RecycleGrowthBytes
	RecycleGrowthBytes = 1 << 20 // recycle if an instance grew >1 MiB over baseline
	defer func() { RecycleGrowthBytes = old }()

	// A big parse balloons linear memory far past baseline → must recycle.
	// ~500 KiB ≈ the indexer's maxContentBytes cap; grows an instance by
	// ~40-60 MiB, comfortably under MemLimitPages but far over the forced
	// 1 MiB recycle threshold above.
	big := []byte(strings.Repeat("func F(){ x:=1; _=x }\n", 24000)) // ~520 KiB
	before := PoolStats()
	for range 3 {
		if _, err := ParseNodes("tree_sitter_go", big); err != nil {
			t.Fatal(err)
		}
	}
	after := PoolStats()

	if after.Recycled-before.Recycled < 3 {
		t.Errorf("expected ≥3 recycles, got %d", after.Recycled-before.Recycled)
	}
	t.Logf("stats: created=%d closed=%d recycled=%d liveOpen=%d idle=%d",
		after.Created, after.Closed, after.Recycled, after.LiveOpen, after.IdlePooled)
}

// TestPool_ReuseWhenSmall verifies that normal (small) parses pool-and-reuse the
// instance rather than recycling — created count must NOT grow once warm.
func TestPool_ReuseWhenSmall(t *testing.T) {
	// default RecycleBytes (64 MiB) — a tiny parse stays well under it.
	_, _ = ParseNodes("tree_sitter_go", []byte("package m\n")) // warm the pool
	mid := PoolStats()
	for range 10 {
		if _, err := ParseNodes("tree_sitter_go", []byte("package m\nfunc G(){}\n")); err != nil {
			t.Fatal(err)
		}
	}
	after := PoolStats()
	if after.Created != mid.Created {
		t.Errorf("warm pool should reuse instances, but Created grew %d→%d", mid.Created, after.Created)
	}
}

// TestParseNodes_Concurrent exercises the engine pool under concurrency — each
// goroutine must get its own instance (wazero modules are not concurrency-safe).
func TestParseNodes_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	src := []byte(strings.Repeat("func F(){}\n", 50))
	for range 16 {
		wg.Go(func() {
			for range 10 {
				if _, err := ParseNodes("tree_sitter_go", src); err != nil {
					t.Errorf("parse: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestInstanceSemaphore(t *testing.T) {
	if _, err := ParseNodes("tree_sitter_go", []byte("package m\n")); err != nil {
		t.Fatal(err) // triggers init
	}
	if instLim == nil {
		t.Fatal("limiter not initialized")
	}
	instLim.mu.Lock()
	max, inUse := instLim.max, instLim.inUse
	instLim.mu.Unlock()
	if max != MaxConcurrentInstances {
		t.Errorf("limiter max = %d, want MaxConcurrentInstances = %d", max, MaxConcurrentInstances)
	}
	if inUse != 0 {
		t.Errorf("limiter should be drained after release, got %d in use", inUse)
	}
}

func TestSetMaxConcurrent_Live(t *testing.T) {
	if _, err := ParseNodes("tree_sitter_go", []byte("package m\n")); err != nil {
		t.Fatal(err) // ensure init
	}
	orig := MaxConcurrentInstances
	defer SetMaxConcurrent(orig)

	SetMaxConcurrent(7)
	instLim.mu.Lock()
	max := instLim.max
	instLim.mu.Unlock()
	if max != 7 || MaxConcurrentInstances != 7 {
		t.Errorf("live resize failed: limiter.max=%d MaxConcurrentInstances=%d, want 7", max, MaxConcurrentInstances)
	}
	// still parses correctly after a live resize
	if _, err := ParseNodes("tree_sitter_go", []byte("package m\nfunc F(){}\n")); err != nil {
		t.Fatalf("parse after resize: %v", err)
	}
}
