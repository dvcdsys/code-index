package client

import "errors"

// ProgressEvent mirrors server/internal/indexer/progress.go:ProgressEvent.
// Both sides ship in the same PR; the duplication is the cost of keeping
// CLI and server as separate Go modules.
//
// Event values: file_started, file_chunked, file_embedded, file_done,
// file_error, heartbeat, batch_done, error.
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

	// batch_done summary.
	FilesAccepted       int `json:"files_accepted,omitempty"`
	ChunksCreated       int `json:"chunks_created,omitempty"`
	FilesProcessedTotal int `json:"files_processed_total,omitempty"`

	RunID string `json:"run_id,omitempty"`
}

// Event kinds — keep in sync with server/internal/indexer/progress.go.
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

// ErrLegacyServer is returned by SendFilesStreaming when the server responds
// with a non-NDJSON Content-Type — meaning the server predates the streaming
// protocol. Callers should surface this as "upgrade your server" rather than
// silently retrying or falling back.
var ErrLegacyServer = errors.New(
	"server does not support streaming protocol — upgrade server to a version that supports NDJSON on /index/files",
)

// ErrIdleTimeout is returned when the streaming response has been silent for
// longer than the configured idle timeout. The server should be sending at
// least a heartbeat every 10 seconds; 30 seconds of silence implies the
// server is hung or the network has stalled.
var ErrIdleTimeout = errors.New("streaming response idle timeout — no data from server")
