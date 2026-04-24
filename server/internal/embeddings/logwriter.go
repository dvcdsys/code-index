package embeddings

import (
	"bytes"
	"context"
	"log/slog"
)

// logWriter is an io.Writer shim that forwards each line from a child process'
// stdout/stderr into our slog logger with a stable `source` attribute. This
// keeps llama-server's output uniform with the rest of the server logs — no
// raw prints hitting the parent's stdout.
//
// Lines longer than the internal buffer are split at chunk boundaries; that is
// acceptable for llama-server which emits short log lines and JSON blobs that
// our log aggregator can parse after the fact.
type logWriter struct {
	logger *slog.Logger
	level  slog.Level
	source string
	buf    []byte
}

func newLogWriter(logger *slog.Logger, level slog.Level, source string) *logWriter {
	return &logWriter{logger: logger, level: level, source: source}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := bytes.TrimRight(w.buf[:idx], "\r")
		if len(line) > 0 {
			// Pass a real context. slog.Log with nil triggers a vet warning
			// and some slog handlers dereference the ctx on every call.
			w.logger.Log(context.Background(), w.level, string(line), "source", w.source)
		}
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

// Close flushes any buffered partial line before the writer is dropped.
// Called when the parent reaps the child process — llama-server sometimes
// crashes before emitting a trailing newline and without this we silently
// drop the last (often most useful) line from the crash log. n1 fix.
func (w *logWriter) Close() error {
	if len(w.buf) > 0 {
		line := bytes.TrimRight(w.buf, "\r")
		if len(line) > 0 {
			w.logger.Log(context.Background(), w.level, string(line), "source", w.source, "partial", true)
		}
		w.buf = nil
	}
	return nil
}
