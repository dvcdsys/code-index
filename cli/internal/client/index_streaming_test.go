package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamWriter is a tiny convenience for tests that need to push NDJSON
// lines from the server side. It writes one JSON object per call followed
// by a newline, then flushes so the client sees it immediately.
type streamWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newStreamWriter(t *testing.T, w http.ResponseWriter) *streamWriter {
	t.Helper()
	f, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("response writer does not implement Flusher")
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &streamWriter{w: w, f: f}
}

func (s *streamWriter) write(t *testing.T, ev ProgressEvent) {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		t.Logf("write: %v", err) // not fatal — client may have disconnected
	}
	s.f.Flush()
}

// TestSendFilesStreaming_BatchDone — happy path: events delivered in order,
// final SendFilesResponse pulled from batch_done event.
func TestSendFilesStreaming_BatchDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/x-ndjson" {
			t.Errorf("Accept header = %q, want application/x-ndjson", r.Header.Get("Accept"))
		}
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{Event: EventFileStarted, Path: "/p/a.go", FileIndex: 1, BatchSize: 2})
		s.write(t, ProgressEvent{Event: EventFileEmbedded, Path: "/p/a.go", Chunks: 3, EmbedMS: 50})
		s.write(t, ProgressEvent{Event: EventFileDone, Path: "/p/a.go", Chunks: 3})
		s.write(t, ProgressEvent{Event: EventFileStarted, Path: "/p/b.go", FileIndex: 2, BatchSize: 2})
		s.write(t, ProgressEvent{Event: EventFileDone, Path: "/p/b.go", Chunks: 2})
		s.write(t, ProgressEvent{
			Event: EventBatchDone, FilesAccepted: 2, ChunksCreated: 5, FilesProcessedTotal: 2,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	var events []ProgressEvent
	resp, err := c.SendFilesStreaming(context.Background(), "/p", "run-1", []FilePayload{
		{Path: "/p/a.go", Content: "x"},
		{Path: "/p/b.go", Content: "y"},
	}, func(ev ProgressEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FilesAccepted != 2 || resp.ChunksCreated != 5 {
		t.Errorf("resp = %+v, want files=2 chunks=5", resp)
	}
	if len(events) != 6 {
		t.Errorf("events count = %d, want 6", len(events))
	}
	if events[0].Event != EventFileStarted {
		t.Errorf("events[0] = %q, want %q", events[0].Event, EventFileStarted)
	}
	if events[len(events)-1].Event != EventBatchDone {
		t.Errorf("last event = %q, want %q", events[len(events)-1].Event, EventBatchDone)
	}
}

// TestSendFilesStreaming_Heartbeat verifies heartbeat events make it to the
// callback (not just dropped) and final result still reflects only batch_done.
func TestSendFilesStreaming_Heartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{Event: EventHeartbeat, TS: "2026-04-27T17:00:00Z"})
		s.write(t, ProgressEvent{Event: EventHeartbeat, TS: "2026-04-27T17:00:10Z"})
		s.write(t, ProgressEvent{Event: EventBatchDone, FilesAccepted: 0})
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	heartbeatCount := 0
	resp, err := c.SendFilesStreaming(context.Background(), "/p", "r", nil, func(ev ProgressEvent) {
		if ev.Event == EventHeartbeat {
			heartbeatCount++
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heartbeatCount != 2 {
		t.Errorf("heartbeat count = %d, want 2", heartbeatCount)
	}
	if resp.FilesAccepted != 0 {
		t.Errorf("resp.FilesAccepted = %d, want 0", resp.FilesAccepted)
	}
}

// TestSendFilesStreaming_LegacyServer ensures we hard-fail when the server
// returns single-JSON instead of NDJSON. No silent fallback — caller learns
// they need to upgrade.
func TestSendFilesStreaming_LegacyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"files_accepted":1,"chunks_created":3,"files_processed_total":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	calledBack := false
	_, err := c.SendFilesStreaming(context.Background(), "/p", "r", nil, func(ev ProgressEvent) {
		calledBack = true
	})
	if !errors.Is(err, ErrLegacyServer) {
		t.Errorf("err = %v, want ErrLegacyServer", err)
	}
	if calledBack {
		t.Error("onEvent should not have been called against a legacy server")
	}
}

// TestSendFilesStreaming_IdleTimeout — stall the response indefinitely and
// confirm the watchdog cancels the request.
func TestSendFilesStreaming_IdleTimeout(t *testing.T) {
	stall := make(chan struct{}) // never closed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := newStreamWriter(t, w)
		// Send one event then sit silent until the client times out.
		s.write(t, ProgressEvent{Event: EventFileStarted, Path: "/p/x.go"})
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stall)

	c := New(srv.URL, "")
	c.SetStreamingIdleTimeout(150 * time.Millisecond)
	_, err := c.SendFilesStreaming(context.Background(), "/p", "r", nil, nil)
	if !errors.Is(err, ErrIdleTimeout) {
		t.Errorf("err = %v, want ErrIdleTimeout", err)
	}
}

// TestSendFilesStreaming_FatalError — server emits a fatal error event,
// caller gets a non-nil error containing the message.
func TestSendFilesStreaming_FatalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{Event: EventFileStarted, Path: "/p/x.go"})
		s.write(t, ProgressEvent{Event: EventError, Message: "embedder unavailable", Fatal: true})
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	_, err := c.SendFilesStreaming(context.Background(), "/p", "r", nil, nil)
	if err == nil {
		t.Fatal("expected error from fatal event, got nil")
	}
	if !strings.Contains(err.Error(), "embedder unavailable") {
		t.Errorf("error %q does not contain server message", err)
	}
}

// TestSendFilesStreaming_NonStreamingErrorBodyDecoded — when the server
// returns non-200 (e.g. 404 bad run_id), the JSON detail is surfaced.
func TestSendFilesStreaming_NonStreamingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"unknown run_id"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	_, err := c.SendFilesStreaming(context.Background(), "/p", "r", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown run_id") {
		t.Errorf("error %q does not surface server detail", err)
	}
}

// TestSendFiles_BackwardCompat — existing public surface still works,
// invoking SendFilesStreaming under the hood.
func TestSendFiles_BackwardCompat(t *testing.T) {
	var requestSeen sync.WaitGroup
	requestSeen.Add(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer requestSeen.Done()
		if r.Header.Get("Accept") != "application/x-ndjson" {
			t.Errorf("SendFiles wrapper must request NDJSON, got Accept=%q", r.Header.Get("Accept"))
		}
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{
			Event: EventBatchDone, FilesAccepted: 1, ChunksCreated: 4, FilesProcessedTotal: 1,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	resp, err := c.SendFiles("/p", "r", []FilePayload{{Path: "/p/x.go"}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.FilesAccepted != 1 || resp.ChunksCreated != 4 {
		t.Errorf("resp = %+v", resp)
	}
	requestSeen.Wait()
}

// TestSendFilesStreaming_RetryOn503 — server returns 503 with Retry-After,
// then succeeds; client should follow the retry and ultimately get the
// batch_done event without surfacing the temporary failure.
func TestSendFilesStreaming_RetryOn503(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		if current == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"detail":"GPU busy"}`))
			return
		}
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{
			Event: EventBatchDone, FilesAccepted: 1, ChunksCreated: 2, FilesProcessedTotal: 1,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	// Stub stdout via the Bash tool not relevant here; the retry print is OK.
	resp, err := c.SendFilesStreaming(context.Background(), "/p", "r", []FilePayload{{Path: "x"}}, nil)
	if err != nil {
		t.Fatalf("expected success after retry, got err: %v", err)
	}
	if resp.FilesAccepted != 1 {
		t.Errorf("resp.FilesAccepted = %d, want 1", resp.FilesAccepted)
	}
	if calls != 2 {
		t.Errorf("expected 2 server calls, got %d", calls)
	}
}

// TestSendFilesStreaming_CallerCancel — caller cancels ctx mid-stream, the
// streaming call returns the context error promptly.
func TestSendFilesStreaming_CallerCancel(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := newStreamWriter(t, w)
		s.write(t, ProgressEvent{Event: EventFileStarted, Path: "/p/x.go"})
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(hold)

	c := New(srv.URL, "")
	c.SetStreamingIdleTimeout(0) // disable watchdog so we test caller cancel only

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after the first event is observed.
	gotEvent := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		_, err := c.SendFilesStreaming(ctx, "/p", "r", nil, func(ev ProgressEvent) {
			select {
			case gotEvent <- struct{}{}:
			default:
			}
		})
		errCh <- err
	}()

	select {
	case <-gotEvent:
	case <-time.After(2 * time.Second):
		t.Fatal("never received first event")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendFilesStreaming did not return after cancel")
	}
}

// keep imports honest for a future addition; small no-op compile guard
var _ = bufio.NewScanner
var _ = io.EOF
var _ = fmt.Sprintf
