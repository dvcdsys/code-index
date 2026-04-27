package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/indexer"
)

// slogDiscard returns a logger that discards all output — used to keep test
// stdout quiet while still satisfying the non-nil Logger contract some code
// paths rely on (Warn/Debug/Info on a nil *slog.Logger panics).
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// flushRecorder is an http.ResponseWriter that supports http.Flusher and
// records every write so tests can observe streamed output. It is safe to
// access from a single test goroutine plus the handler goroutine — the
// shared buffer is mutex-protected.
type flushRecorder struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	header  http.Header
	status  int
	written chan struct{}
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{
		header:  make(http.Header),
		written: make(chan struct{}, 1),
	}
}

func (r *flushRecorder) Header() http.Header { return r.header }

func (r *flushRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	n, err := r.buf.Write(p)
	r.mu.Unlock()
	select {
	case r.written <- struct{}{}:
	default:
	}
	return n, err
}

func (r *flushRecorder) WriteHeader(s int) { r.status = s }

func (r *flushRecorder) Flush() {} // no-op — buf is already coherent

// waitForBytes blocks until the recorder accumulates at least min bytes or
// the timeout elapses. Returns true on success.
func (r *flushRecorder) waitForBytes(timeout time.Duration, min int) bool {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		got := r.buf.Len()
		r.mu.Unlock()
		if got >= min {
			return true
		}
		select {
		case <-r.written:
			// loop
		case <-deadline:
			return false
		}
	}
}

// streamingTestServer spins up a real httptest.Server so the streaming
// handler gets an http.ResponseWriter that implements Flusher (which
// httptest.ResponseRecorder does not).
func streamingTestServer(t *testing.T, projectPath string) (*httptest.Server, string) {
	t.Helper()
	d, hash := newIndexerTestDeps(t, projectPath)
	srv := httptest.NewServer(NewRouter(d))
	t.Cleanup(srv.Close)
	return srv, hash
}

// blockingEmbedder is a fakeEmbedder that waits on a channel before returning.
// Used to simulate a slow embedder so the disconnect test can interrupt mid-batch
// before ProcessFilesStreaming completes naturally.
type blockingEmbedder struct {
	fakeEmbedder
	release chan struct{} // close to allow EmbedTexts to proceed
}

func (b *blockingEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakeEmbedder.EmbedTexts(ctx, texts)
}


// readNDJSONLines reads NDJSON until either io.EOF or until limit lines have
// been collected. Returns the parsed events.
func readNDJSONLines(t *testing.T, body io.Reader, limit int) []indexer.ProgressEvent {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var events []indexer.ProgressEvent
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev indexer.ProgressEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("decode ndjson line %q: %v", line, err)
		}
		events = append(events, ev)
		if limit > 0 && len(events) >= limit {
			return events
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Fatalf("scan: %v", err)
	}
	return events
}

// beginSession is a small helper: starts a session, returns run_id.
func beginSession(t *testing.T, baseURL, hash string) string {
	t.Helper()
	resp, err := http.Post(
		baseURL+"/api/v1/projects/"+hash+"/index/begin",
		"application/json",
		strings.NewReader(`{"full":true}`),
	)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("begin status=%d body=%s", resp.StatusCode, body)
	}
	var br indexBeginResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		t.Fatalf("decode begin: %v", err)
	}
	return br.RunID
}

func newFilesRequestBody(t *testing.T, runID string, files map[string]string) []byte {
	t.Helper()
	payload := map[string]any{
		"run_id": runID,
		"files":  []map[string]any{},
	}
	for path, content := range files {
		payload["files"] = append(payload["files"].([]map[string]any), map[string]any{
			"path":         path,
			"content":      content,
			"content_hash": shaHex(content),
			"language":     "go",
			"size":         len(content),
		})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestIndexFilesStreaming_BatchDone exercises the happy path: NDJSON event
// stream contains file_started + batch_done with the expected counts.
func TestIndexFilesStreaming_BatchDone(t *testing.T) {
	srv, hash := streamingTestServer(t, "/proj")
	runID := beginSession(t, srv.URL, hash)

	body := newFilesRequestBody(t, runID, map[string]string{
		"/proj/a.go": "package main\nfunc A() int { return 1 }\n",
		"/proj/b.go": "package main\nfunc B() int { return 2 }\n",
	})

	req, _ := http.NewRequest(
		http.MethodPost,
		srv.URL+"/api/v1/projects/"+hash+"/index/files",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type=%q, want application/x-ndjson", ct)
	}

	events := readNDJSONLines(t, resp.Body, 0)
	if len(events) == 0 {
		t.Fatal("no events in stream")
	}

	last := events[len(events)-1]
	if last.Event != indexer.EventBatchDone {
		t.Fatalf("last event = %q, want %q (events: %v)", last.Event, indexer.EventBatchDone, summarizeEvents(events))
	}
	if last.FilesAccepted != 2 {
		t.Errorf("files_accepted=%d, want 2", last.FilesAccepted)
	}
	if last.ChunksCreated == 0 {
		t.Errorf("chunks_created=0")
	}

	// At least one file_started event must appear.
	startedCount := 0
	for _, e := range events {
		if e.Event == indexer.EventFileStarted {
			startedCount++
		}
	}
	if startedCount != 2 {
		t.Errorf("file_started count=%d, want 2", startedCount)
	}
}

// TestIndexFilesStreaming_LegacyCompat verifies that requests without an
// Accept: application/x-ndjson header keep getting the existing single-JSON
// response. This is the regression guard for old CLIs against a new server.
func TestIndexFilesStreaming_LegacyCompat(t *testing.T) {
	srv, hash := streamingTestServer(t, "/proj")
	runID := beginSession(t, srv.URL, hash)

	body := newFilesRequestBody(t, runID, map[string]string{
		"/proj/x.go": "package main\nfunc X() {}\n",
	})

	req, _ := http.NewRequest(
		http.MethodPost,
		srv.URL+"/api/v1/projects/"+hash+"/index/files",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	// No Accept header → legacy path.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, out)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q, want application/json (legacy)", ct)
	}

	var legacy indexFilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&legacy); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if legacy.FilesAccepted != 1 {
		t.Errorf("files_accepted=%d, want 1", legacy.FilesAccepted)
	}
}

// TestIndexFilesStreaming_AcceptOnly verifies that Accept headers without
// application/x-ndjson (e.g. */*) take the legacy path — only an explicit
// streaming opt-in upgrades the protocol.
func TestIndexFilesStreaming_AcceptOnly(t *testing.T) {
	srv, hash := streamingTestServer(t, "/proj")
	runID := beginSession(t, srv.URL, hash)

	body := newFilesRequestBody(t, runID, map[string]string{
		"/proj/y.go": "package main\nfunc Y() {}\n",
	})

	req, _ := http.NewRequest(
		http.MethodPost,
		srv.URL+"/api/v1/projects/"+hash+"/index/files",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Accept=*/* should still get legacy JSON, got Content-Type=%q", ct)
	}
}

// TestIndexFilesStreaming_ClientDisconnect verifies that cancelling the
// request context mid-batch frees the session lock. We call the handler
// directly with a context we control rather than relying on Go's net/http
// to detect a client TCP disconnect — that detection is best-effort and
// unreliable in unit-test timeframes (it depends on the OS noticing FIN
// during a write or read goroutine, which can take seconds with chunked
// encoding even when the client has already closed). Cancelling the
// request's context is the same signal the server reacts to in production
// (chi propagates it from the underlying http.Request).
func TestIndexFilesStreaming_ClientDisconnect(t *testing.T) {
	// Heartbeat shrunk so the inner ticker case fires reliably during the test.
	prevHB := streamingHeartbeatInterval
	streamingHeartbeatInterval = 50 * time.Millisecond
	t.Cleanup(func() { streamingHeartbeatInterval = prevHB })

	emb := &blockingEmbedder{
		fakeEmbedder: fakeEmbedder{dim: 16},
		release:      make(chan struct{}),
	}
	d, hash := newIndexerTestDeps(t, "/proj")
	d.EmbeddingSvc = emb
	d.Indexer = indexer.New(d.DB, d.VectorStore, emb, slogDiscard())
	d.Logger = slogDiscard()
	router := NewRouter(d)

	// Begin a session so we have a valid run_id.
	beginW := httptest.NewRecorder()
	beginReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+hash+"/index/begin",
		strings.NewReader(`{"full":true}`))
	beginReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(beginW, beginReq)
	if beginW.Code != 200 {
		t.Fatalf("begin: status=%d body=%s", beginW.Code, beginW.Body)
	}
	var br indexBeginResponse
	_ = json.Unmarshal(beginW.Body.Bytes(), &br)

	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files[fmt.Sprintf("/proj/file_%d.go", i)] =
			"package main\nfunc F() int { return 1 }\n"
	}
	body := newFilesRequestBody(t, br.RunID, files)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+hash+"/index/files",
		bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")

	rw := newFlushRecorder()

	// Serve in a goroutine because the handler will block on the embedder
	// until we cancel ctx.
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rw, req)
		close(done)
	}()

	// Wait for the first NDJSON line to reach our recorder — proves the
	// handler is running and ProcessFilesStreaming is engaged.
	if !rw.waitForBytes(2*time.Second, 10) {
		t.Fatal("no bytes written before disconnect deadline")
	}

	// Disconnect: the request ctx is what chi passes through to r.Context().
	cancel()

	// Handler must return promptly after ctx cancel.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return within 2s after ctx cancel")
	}

	// A new /index/begin must succeed: proves CancelIndexing was called.
	begin2W := httptest.NewRecorder()
	begin2Req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+hash+"/index/begin",
		strings.NewReader(`{"full":true}`))
	begin2Req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(begin2W, begin2Req)
	if begin2W.Code != 200 {
		t.Fatalf("begin after disconnect: status=%d body=%s — session lock not released",
			begin2W.Code, begin2W.Body)
	}
}

// TestAcceptsNDJSON unit-tests the Accept header parser.
func TestAcceptsNDJSON(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"application/x-ndjson", true},
		{"application/x-ndjson; q=1.0", true},
		{"application/json, application/x-ndjson", true},
		{"  application/x-ndjson  ", true},
		{"application/X-NDJSON", true}, // case-insensitive
		{"*/*", false},
		{"application/json", false},
		{"", false},
	}
	for _, c := range cases {
		if got := acceptsNDJSON(c.header); got != c.want {
			t.Errorf("acceptsNDJSON(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func summarizeEvents(events []indexer.ProgressEvent) string {
	var b strings.Builder
	for i, e := range events {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(e.Event)
	}
	return b.String()
}
