package embeddings

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/config"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
	"github.com/dvcdsys/code-index/server/internal/vectorstore"
)

// fakeProv is a minimal provider.Provider for exercising the vector-store
// reopen path; only ID() is consulted by reopenVectorStore / StorageSlug.
type fakeProv struct{ id string }

func (f fakeProv) Kind() string                                          { return "fake" }
func (f fakeProv) ID() string                                            { return f.id }
func (f fakeProv) Dimension() int                                        { return 0 }
func (f fakeProv) SupportsTokenize() bool                                { return false }
func (f fakeProv) Start(context.Context) error                           { return nil }
func (f fakeProv) Stop(context.Context) error                            { return nil }
func (f fakeProv) Ready(context.Context) error                           { return nil }
func (f fakeProv) Status() provider.Status                               { return provider.Status{} }
func (f fakeProv) EmbedQuery(context.Context, string) ([]float32, error) { return nil, nil }
func (f fakeProv) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (f fakeProv) TokenizeAndEmbed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestServiceStorageSlug(t *testing.T) {
	s := &Service{logger: quiet(), current: fakeProv{id: "voyage:voyage-code-3:2048:float"}}
	if got := s.StorageSlug(); got != "voyage_voyage_code_3_2048_float" {
		t.Errorf("StorageSlug = %q", got)
	}
	// Disabled / no provider → empty.
	if got := (&Service{logger: quiet(), disabled: true}).StorageSlug(); got != "" {
		t.Errorf("disabled StorageSlug = %q, want empty", got)
	}
	if got := (&Service{logger: quiet()}).StorageSlug(); got != "" {
		t.Errorf("nil-provider StorageSlug = %q, want empty", got)
	}
}

func TestReopenVectorStore_SwapsToNewNamespace(t *testing.T) {
	dir := t.TempDir()
	const project = "/proj"

	// Initial store has one chunk for the project.
	initial, err := vectorstore.Open(filepath.Join(dir, "chroma_ollama_m"))
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.UpsertChunks(context.Background(), project,
		[]vectorstore.Chunk{{Content: "x", FilePath: "a.go", StartLine: 1, EndLine: 1, Language: "go"}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	holder := vectorstore.NewHolder(initial)
	if holder.Count(project) != 1 {
		t.Fatalf("precondition: initial holder count != 1")
	}

	s := &Service{logger: quiet()}
	s.AttachVectorStore(
		holder,
		func(slug string) string { return filepath.Join(dir, "chroma_"+slug) },
		vectorstore.Open,
		nil,
	)

	// Switch to a new identity → reopen into a fresh, empty namespace.
	if err := s.reopenVectorStore(fakeProv{id: "voyage:voyage-code-3:2048:float"}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := holder.Count(project); got != 0 {
		t.Errorf("after reopen Count = %d, want 0 (new empty namespace)", got)
	}
	// New dir created on disk; old dir still present (reuse on switch back).
	if !dirExists(filepath.Join(dir, "chroma_voyage_voyage_code_3_2048_float")) {
		t.Errorf("new chroma dir should exist")
	}
	if !dirExists(filepath.Join(dir, "chroma_ollama_m")) {
		t.Errorf("old chroma dir should be preserved")
	}
}

func TestReopenVectorStore_OpenerFailureKeepsOldStore(t *testing.T) {
	dir := t.TempDir()
	const project = "/proj"
	initial, err := vectorstore.Open(filepath.Join(dir, "chroma_ollama_m"))
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.UpsertChunks(context.Background(), project,
		[]vectorstore.Chunk{{Content: "x", FilePath: "a.go", StartLine: 1, EndLine: 1, Language: "go"}},
		[][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	holder := vectorstore.NewHolder(initial)

	s := &Service{logger: quiet()}
	s.AttachVectorStore(
		holder,
		func(slug string) string { return filepath.Join(dir, "chroma_"+slug) },
		func(string) (*vectorstore.Store, error) { return nil, errors.New("boom") },
		nil,
	)

	err = s.reopenVectorStore(fakeProv{id: "voyage:m:2048:float"})
	if err == nil {
		t.Fatal("expected reopen error")
	}
	// Holder must still serve the OLD store (no Swap on failure).
	if got := holder.Count(project); got != 1 {
		t.Errorf("after failed reopen Count = %d, want 1 (old store retained)", got)
	}
}

func TestReopenVectorStore_NoopWhenUnwired(t *testing.T) {
	// A Service without AttachVectorStore must not panic / error.
	s := &Service{logger: quiet()}
	if err := s.reopenVectorStore(fakeProv{id: "voyage:m:2048:float"}); err != nil {
		t.Errorf("unwired reopen should be a no-op, got %v", err)
	}
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// TestRestart_ConcurrentWithEmbeds_NoRace guards H1: Restart swaps the
// s.queue pointer when the concurrency cap changes, while Embed* callers
// read it to acquire/release slots. Run under -race with many embedders
// hammering the queue while a restarter repeatedly swaps it. A remote
// (non-ollama) fake provider keeps Restart on the queue-only path with no
// sidecar to manage.
func TestRestart_ConcurrentWithEmbeds_NoRace(t *testing.T) {
	s := &Service{
		logger:  quiet(),
		queue:   NewQueue(2, time.Second),
		current: fakeProv{id: "fake:m"},
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = s.EmbedTexts(context.Background(), []string{"x"})
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			// Alternate the cap so every other Restart actually swaps the
			// queue pointer (the racy write H1 fixes).
			n := 2 + (i % 3) // 2, 3, 4
			_ = s.Restart(context.Background(), &config.Config{
				MaxEmbeddingConcurrency: n,
				EmbeddingQueueTimeout:   1,
			})
		}
		close(stop)
	}()

	wg.Wait()
}
