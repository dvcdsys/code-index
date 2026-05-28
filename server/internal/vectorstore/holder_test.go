package vectorstore

import (
	"context"
	"sync"
	"testing"
)

func storeWithOneChunk(t *testing.T, project string) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	chunks := []Chunk{{
		Content: "hello", FilePath: "a.go", StartLine: 1, EndLine: 2, Language: "go",
	}}
	embs := [][]float32{{1, 0, 0, 0}}
	if err := s.UpsertChunks(context.Background(), project, chunks, embs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return s
}

func emptyStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func TestHolderProxyAndSwap(t *testing.T) {
	const project = "/proj"
	a := storeWithOneChunk(t, project)
	b := emptyStore(t)

	h := NewHolder(a)
	if got := h.Count(project); got != 1 {
		t.Fatalf("Count via holder = %d, want 1", got)
	}

	old := h.Swap(b)
	if old != a {
		t.Errorf("Swap should return the previous store")
	}
	if got := h.Count(project); got != 0 {
		t.Errorf("Count after swap to empty store = %d, want 0", got)
	}
}

func TestHolderNilGuards(t *testing.T) {
	h := NewHolder(nil)
	if got := h.Count("/p"); got != 0 {
		t.Errorf("nil-store Count = %d, want 0", got)
	}
	res, err := h.Search(context.Background(), "/p", []float32{1, 0}, 5, nil)
	if err != nil || res != nil {
		t.Errorf("nil-store Search = (%v, %v), want (nil, nil)", res, err)
	}
	if err := h.UpsertChunks(context.Background(), "/p", nil, nil); err == nil {
		t.Errorf("nil-store UpsertChunks should error")
	}
	if err := h.DeleteByFile(context.Background(), "/p", "f"); err == nil {
		t.Errorf("nil-store DeleteByFile should error")
	}
	if err := h.DeleteCollection("/p"); err == nil {
		t.Errorf("nil-store DeleteCollection should error")
	}
}

// TestHolderConcurrentSwap runs under -race: many goroutines Search/Count
// while another goroutine repeatedly Swaps between two valid stores. The
// RWMutex must guarantee no torn pointer / data race.
func TestHolderConcurrentSwap(t *testing.T) {
	const project = "/proj"
	a := storeWithOneChunk(t, project)
	b := storeWithOneChunk(t, project)
	h := NewHolder(a)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = h.Count(project)
					_, _ = h.Search(context.Background(), project, []float32{1, 0, 0, 0}, 1, nil)
				}
			}
		}()
	}
	// Swapper.
	wg.Add(1)
	go func() {
		defer wg.Done()
		stores := []*Store{a, b}
		for i := 0; i < 2000; i++ {
			h.Swap(stores[i%2])
		}
		close(stop)
	}()

	wg.Wait()
}
