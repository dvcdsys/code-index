package repolocks

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestWithWrite_ReleasesOnPanic is the regression guard for the clone handler
// bug: a panic inside the locked region must still release the write-lock (via
// defer), otherwise every later reader/writer for that repo deadlocks forever.
func TestWithWrite_ReleasesOnPanic(t *testing.T) {
	l := New()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected the panic to propagate out of WithWrite")
			}
		}()
		_ = l.WithWrite("repo", func() error {
			panic("boom in the middle of a worktree reset")
		})
	}()

	// The lock must be free now. TryLock returns false if it is still held.
	if !l.For("repo").TryLock() {
		t.Fatal("write-lock leaked after a panicking WithWrite — subsequent clones would deadlock")
	}
	l.For("repo").Unlock()
}

// TestWithRead_ReleasesOnPanic mirrors the above for the read side (the indexer
// walk): a panic mid-walk must not leave the read-lock held.
func TestWithRead_ReleasesOnPanic(t *testing.T) {
	l := New()

	func() {
		defer func() { _ = recover() }()
		_ = l.WithRead("repo", func() error { panic("boom mid-walk") })
	}()

	// A held read-lock would block TryLock (write).
	if !l.For("repo").TryLock() {
		t.Fatal("read-lock leaked after a panicking WithRead")
	}
	l.For("repo").Unlock()
}

// TestWithWrite_PropagatesResultAndError confirms the helper returns fn's error
// and lets fn publish a value through a captured variable (the pattern the clone
// handler uses for repocloner.Result).
func TestWithWrite_PropagatesResultAndError(t *testing.T) {
	l := New()
	want := errors.New("clone failed")

	var captured int
	got := l.WithWrite("repo", func() error {
		captured = 42
		return want
	})
	if !errors.Is(got, want) {
		t.Errorf("WithWrite returned %v, want %v", got, want)
	}
	if captured != 42 {
		t.Errorf("captured = %d, want 42 (fn must run under the lock)", captured)
	}
}

// TestRead_BlocksWrite is the finding-7 invariant: while a reader (the indexer)
// holds the read-lock, the writer (the clone worker) cannot rewrite the
// worktree — WithWrite blocks until the read releases.
func TestRead_BlocksWrite(t *testing.T) {
	l := New()

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	writeAcquired := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = l.WithRead("repo", func() error {
			close(readEntered)
			<-releaseRead // hold the read-lock until the test says so
			return nil
		})
	}()

	<-readEntered

	go func() {
		_ = l.WithWrite("repo", func() error {
			close(writeAcquired)
			return nil
		})
	}()

	// The writer must NOT get the lock while the reader holds it.
	select {
	case <-writeAcquired:
		t.Fatal("writer acquired the lock while a reader held it — indexer/clone would race")
	case <-time.After(100 * time.Millisecond):
		// expected: writer is blocked
	}

	close(releaseRead) // let the reader finish

	select {
	case <-writeAcquired:
		// expected: writer proceeds once the reader released
	case <-time.After(2 * time.Second):
		t.Fatal("writer never acquired the lock after the reader released")
	}
	wg.Wait()
}

// TestConcurrentReads_DontBlock confirms two readers (e.g. a file read and the
// indexer) proceed in parallel — the lock only excludes the writer.
func TestConcurrentReads_DontBlock(t *testing.T) {
	l := New()

	firstIn := make(chan struct{})
	bothIn := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = l.WithRead("repo", func() error {
			close(firstIn)
			<-bothIn
			return nil
		})
	}()
	<-firstIn
	go func() {
		defer wg.Done()
		_ = l.WithRead("repo", func() error {
			close(bothIn) // only reachable if this read wasn't blocked by the first
			return nil
		})
	}()

	select {
	case <-bothIn:
		// expected: the second reader entered while the first was still holding
	case <-time.After(2 * time.Second):
		t.Fatal("a second reader was blocked by the first — reads must not exclude reads")
	}
	wg.Wait()
}

// TestDifferentKeys_Independent confirms locks are per-repo: a write on one repo
// never blocks a write on another.
func TestDifferentKeys_Independent(t *testing.T) {
	l := New()

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = l.WithWrite("repoA", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	done := make(chan struct{})
	go func() {
		_ = l.WithWrite("repoB", func() error { return nil })
		close(done)
	}()

	select {
	case <-done:
		// expected: repoB's write is independent of repoA's
	case <-time.After(2 * time.Second):
		t.Fatal("write on repoB blocked on repoA's lock — keys must be independent")
	}
	close(release)
}
