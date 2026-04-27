package indexer

import "time"

// ProgressEvent is emitted by ProcessFiles when a non-nil progress channel is
// supplied, so the streaming HTTP handler can forward each one as a JSON line.
//
// One struct with all possible fields + omitempty is intentional: it keeps the
// wire format easy to evolve and the consumer code a single switch on Event.
//
// Wire format example (newline-delimited JSON, one struct per line):
//
//	{"event":"file_started","run_id":"...","path":"main.go","file_index":1,"batch_size":20}
//	{"event":"file_chunked","path":"main.go","chunks":12}
//	{"event":"file_embedded","path":"main.go","chunks":12,"embed_ms":540}
//	{"event":"file_done","path":"main.go","chunks":12}
//	{"event":"heartbeat","ts":"2026-04-27T17:25:00Z"}
//	{"event":"file_error","path":"big.bin","message":"...","fatal":false}
//	{"event":"batch_done","files_accepted":20,"chunks_created":347,"files_processed_total":300}
//	{"event":"error","message":"...","fatal":true}
type ProgressEvent struct {
	Event string `json:"event"`

	// Per-file fields.
	Path      string `json:"path,omitempty"`
	FileIndex int    `json:"file_index,omitempty"`
	BatchSize int    `json:"batch_size,omitempty"`
	Chunks    int    `json:"chunks,omitempty"`
	EmbedMS   int64  `json:"embed_ms,omitempty"`

	// Heartbeat.
	TS string `json:"ts,omitempty"`

	// Errors.
	Message string `json:"message,omitempty"`
	Fatal   bool   `json:"fatal,omitempty"`

	// Batch-done summary (mirrors indexFilesResponse).
	FilesAccepted       int `json:"files_accepted,omitempty"`
	ChunksCreated       int `json:"chunks_created,omitempty"`
	FilesProcessedTotal int `json:"files_processed_total,omitempty"`

	// Run identifier — populated on the first event the handler emits.
	RunID string `json:"run_id,omitempty"`
}

// Event kinds. Using string constants both for documentation and for
// comparisons in tests / consumer switches.
const (
	EventFileStarted  = "file_started"
	EventFileChunked  = "file_chunked"
	EventFileEmbedded = "file_embedded"
	EventFileDone     = "file_done"
	EventFileError    = "file_error"
	EventHeartbeat    = "heartbeat"
	EventBatchDone    = "batch_done"
	EventError        = "error"
)

// progressSend is a nil-safe non-blocking send. ProcessFiles uses it instead
// of `progress <- e` so that:
//
//  1. callers that do not care about progress pass nil and pay no cost
//  2. a slow consumer cannot stall the indexer (the channel has a small
//     buffer in the streaming handler; if it fills we drop the event rather
//     than blocking the embed pipeline)
//
// Drops are acceptable because the only events that *must* land are
// batch_done / error, and those are sent on the unbuffered close path
// using a guaranteed-blocking send (see emitTerminal).
func progressSend(ch chan<- ProgressEvent, e ProgressEvent) {
	if ch == nil {
		return
	}
	select {
	case ch <- e:
	default:
		// channel full — drop. Keeps embed loop unblocked.
	}
}

// emitTerminal is for batch_done / fatal error: must reach the consumer.
// Always blocks until accepted (or ctx cancellation closes things upstream).
func emitTerminal(ch chan<- ProgressEvent, e ProgressEvent) {
	if ch == nil {
		return
	}
	ch <- e
}

// NowTS returns an RFC3339 timestamp for heartbeat events. Exported so the
// streaming HTTP handler in package httpapi can stamp its own heartbeats.
func NowTS() string {
	return time.Now().UTC().Format(time.RFC3339)
}
