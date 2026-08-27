package searchstats

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Recording sits on the search request's critical path, so its cost is a
// property the feature has to keep, not an implementation detail. Everything
// here measures the part a search actually pays for: Record, and nothing else.
// The flush is a background goroutine and shows up only as contention.

func benchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), DBFileName))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func benchFiles(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("internal/pkg%d/file%d.go", i%7, i)
	}
	return out
}

// One search's worth of recording, uncontended. Ten files is a typical
// semantic-search result.
func BenchmarkRecord(b *testing.B) {
	s := benchStore(b)
	r := NewRecorder(s, nil)
	files := benchFiles(10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Record("proj", KindSemantic, files)
	}
}

// The same call under concurrency. Every search on the server shares one mutex,
// so this is the number that says whether the counters serialise search.
func BenchmarkRecordParallel(b *testing.B) {
	for _, projects := range []int{1, 8} {
		b.Run(fmt.Sprintf("projects=%d", projects), func(b *testing.B) {
			s := benchStore(b)
			r := NewRecorder(s, nil)
			files := benchFiles(10)
			b.ReportAllocs()
			b.ResetTimer()
			var n int
			b.RunParallel(func(pb *testing.PB) {
				n++
				project := fmt.Sprintf("proj-%d", n%projects)
				for pb.Next() {
					r.Record(project, KindSemantic, files)
				}
			})
		})
	}
}

// Record while a flusher runs against the same recorder, which is the shape
// production has: a background goroutine taking the same lock every 10 s. If
// the swap-under-lock were doing real work, it would show up here as a gap
// between this and BenchmarkRecordParallel.
func BenchmarkRecordParallelDuringFlush(b *testing.B) {
	s := benchStore(b)
	r := NewRecorder(s, nil)
	files := benchFiles(10)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(2 * time.Millisecond) // far more aggressive than the real 10 s
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = r.Flush(context.Background())
			}
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Record("proj", KindSemantic, files)
		}
	})
	b.StopTimer()
	close(stop)
	<-done
}

// What one flush costs, so the background writer's footprint is known rather
// than assumed. 200 projects x 10 files is a large batch by the standards of a
// 10-second window.
func BenchmarkFlush(b *testing.B) {
	s := benchStore(b)
	r := NewRecorder(s, nil)
	files := benchFiles(10)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for p := 0; p < 200; p++ {
			r.Record(fmt.Sprintf("proj-%d", p), KindSemantic, files)
		}
		b.StartTimer()
		if err := r.Flush(ctx); err != nil {
			b.Fatalf("Flush: %v", err)
		}
	}
}
