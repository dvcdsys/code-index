//go:build unix

package tswasm

// Temporary breakdown harness: where does the memory go?
// Run: CIX_MEMSTRESS=1 go test ./internal/chunker/tswasm -run TestMemBreakdown -v

import (
	"bytes"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/andybalholm/brotli"
)

func report(t *testing.T, tag string) {
	goruntime.GC()
	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	rss := float64(ru.Maxrss) / (1 << 20)
	if goruntime.GOOS != "darwin" {
		rss = float64(ru.Maxrss) / 1024
	}
	ps := PoolStats()
	t.Logf("%-28s heapAlloc=%7.1fMB heapSys=%7.1fMB maxRSS=%7.1fMB | created=%d closed=%d recycled=%d live=%d idle=%d idleMem=%.1fMB",
		tag, float64(ms.HeapAlloc)/(1<<20), float64(ms.HeapSys)/(1<<20), rss,
		ps.Created, ps.Closed, ps.Recycled, ps.LiveOpen, ps.IdlePooled, float64(ps.IdleMemBytes)/(1<<20))
}

func TestMemBreakdown(t *testing.T) {
	if os.Getenv("CIX_MEMSTRESS") == "" {
		t.Skip("set CIX_MEMSTRESS=1 to run")
	}
	report(t, "start")

	// 1. decompress only
	raw, err := io.ReadAll(brotli.NewReader(bytes.NewReader(wasmBrotli)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("raw wasm: %.1fMB", float64(len(raw))/(1<<20))
	report(t, "after decompress")
	raw = nil
	report(t, "after drop raw")

	// 2. compile (via initRuntime)
	rtOnce.Do(initRuntime)
	if rtErr != nil {
		t.Fatal(rtErr)
	}
	report(t, "after CompileModule")

	// 3. one instance
	e, err := newEngine()
	if err != nil {
		t.Fatal(err)
	}
	report(t, "after 1 instance")

	// 4. parse a small file
	if _, err := e.parseNodes("tree_sitter_go", []byte("package m\nfunc F(){}\n")); err != nil {
		t.Fatal(err)
	}
	report(t, "after small parse")

	// 5. parse a big file (~500KB Go) — near the indexer's maxContentBytes cap
	var sb strings.Builder
	sb.WriteString("package m\n")
	for i := 0; sb.Len() < 500*1024; i++ {
		fmt.Fprintf(&sb, "func F%d(a, b int) int { x := a + b; y := x * 2; s := fmt.Sprintf(\"%%d\", y); _ = s; return y }\n", i)
	}
	big := []byte(sb.String())
	nodes, err := e.parseNodes("tree_sitter_go", big)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("big parse: %d nodes from %dKB", len(nodes), len(big)/1024)
	report(t, "after big parse (go)")

	// repeat big parse 10x on same instance — does instance memory keep growing?
	for range 10 {
		if _, err := e.parseNodes("tree_sitter_go", big); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("instance memSize now: %.1fMB (baseline %.1fMB)", float64(e.memSize())/(1<<20), float64(baselineBytes.Load())/(1<<20))
	report(t, "after 10 more big parses")
	e.close()
	report(t, "after close instance")

	// 6. concurrency 3 with big typescript-ish files through the public API
	tsSrc := []byte(strings.Repeat("export function foo(a: number, b: string): Promise<void> { const x = a + b.length; if (x > 0) { console.log(x); } }\nclass Bar { private x: number = 1; method(y: number): number { return this.x + y; } }\n", 1500))
	t.Logf("ts src: %dKB", len(tsSrc)/1024)
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if _, err := ParseNodes("tree_sitter_typescript", tsSrc); err != nil {
					t.Errorf("ts parse: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	report(t, "after 3x20 concurrent big TS")

	// 7. churn: force recycling (big parses with low threshold)
	oldRec := RecycleGrowthBytes
	RecycleGrowthBytes = 8 << 20
	for range 10 {
		if _, err := ParseNodes("tree_sitter_go", big); err != nil {
			t.Fatal(err)
		}
	}
	RecycleGrowthBytes = oldRec
	report(t, "after 10 recycle churns")
}

// TestChurnPlateau: does sustained recycle churn plateau or grow unboundedly?
func TestChurnPlateau(t *testing.T) {
	if os.Getenv("CIX_MEMSTRESS") == "" {
		t.Skip("set CIX_MEMSTRESS=1 to run")
	}
	var sb strings.Builder
	sb.WriteString("package m\n")
	for i := 0; sb.Len() < 500*1024; i++ {
		fmt.Fprintf(&sb, "func F%d(a, b int) int { x := a + b; y := x * 2; s := fmt.Sprintf(\"%%d\", y); _ = s; return y }\n", i)
	}
	big := []byte(sb.String())

	oldRec := RecycleGrowthBytes
	RecycleGrowthBytes = 8 << 20 // force recycle every big parse
	defer func() { RecycleGrowthBytes = oldRec }()

	report(t, "churn start")
	for i := 1; i <= 100; i++ {
		if _, err := ParseNodes("tree_sitter_go", big); err != nil {
			t.Fatal(err)
		}
		if i%20 == 0 {
			report(t, fmt.Sprintf("churn %d", i))
		}
	}
	report(t, "churn end")
}
