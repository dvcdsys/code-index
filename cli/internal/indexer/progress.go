package indexer

import (
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/anthropics/code-index/cli/internal/client"
)

// ProgressMode controls how SendFilesStreaming events are rendered to the
// user. Reindex on a TTY uses Interactive (in-place status line); reindex
// in CI / non-TTY context uses LineByLine; the watcher uses Quiet (only
// summary + errors land in the log).
type ProgressMode int

const (
	// ProgressInteractive updates a single status line with carriage returns.
	ProgressInteractive ProgressMode = iota
	// ProgressLineByLine prints one log line per file_started/file_done.
	ProgressLineByLine
	// ProgressQuiet only prints file_error and the final batch summary.
	ProgressQuiet
)

// AutoProgressMode returns Interactive when stdout is a terminal and
// LineByLine otherwise. Tests and watchers should pass an explicit mode.
func AutoProgressMode() ProgressMode {
	if isTerminal(os.Stdout) {
		return ProgressInteractive
	}
	return ProgressLineByLine
}

// isTerminal reports whether f is a character device (a TTY). Avoids the
// golang.org/x/term dependency to keep the CLI module's go directive at the
// existing minimum (no toolchain bump for a single-line check).
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// progressRenderer is a stateful event handler. It is created once per batch
// (so per-batch counters reset) and called for every NDJSON event the
// streaming client receives.
type progressRenderer struct {
	mode        ProgressMode
	out         io.Writer
	totalFiles  int       // total files to process across all batches
	batchOffset int       // index of the first file in this batch (1-based)
	fileStart   time.Time // when the current file's file_started arrived

	// lastLineRunes tracks the visible width of the last status line we
	// drew so we can erase it before redrawing. We count runes (not bytes)
	// because UTF-8 multi-byte chars like `…` would otherwise inflate the
	// padding and the cursor would land past the visible end, leaving the
	// tail of the previous line on screen.
	lastLineRunes int

	// activeFile is the path we're currently rendering progress for.
	activeFile string

	// activeFileIdx caches the global file index from file_started so it
	// can be reused on file_chunked / file_embedded / file_done — which
	// the server emits without a FileIndex field. Without this cache, the
	// renderer fell back to ev.FileIndex == 0 and printed `[0/N]` on every
	// file_embedded line.
	activeFileIdx int
}

func newProgressRenderer(mode ProgressMode, totalFiles, batchOffset int) *progressRenderer {
	return &progressRenderer{
		mode:        mode,
		out:         os.Stdout,
		totalFiles:  totalFiles,
		batchOffset: batchOffset,
	}
}

// onEvent is the callback fed to client.SendFilesStreaming.
func (r *progressRenderer) onEvent(ev client.ProgressEvent) {
	switch r.mode {
	case ProgressInteractive:
		r.renderInteractive(ev)
	case ProgressLineByLine:
		r.renderLineByLine(ev)
	case ProgressQuiet:
		r.renderQuiet(ev)
	}
}

func (r *progressRenderer) renderInteractive(ev client.ProgressEvent) {
	switch ev.Event {
	case client.EventFileStarted:
		r.activeFile = ev.Path
		r.activeFileIdx = r.batchOffset + ev.FileIndex
		r.fileStart = time.Now()
		r.statusLine(fmt.Sprintf("[%d/%d] %s (chunking…)",
			r.activeFileIdx, r.totalFiles, ev.Path))

	case client.EventFileEmbedded:
		// FileIndex is not populated on file_embedded; reuse the cached
		// value from the matching file_started.
		r.statusLine(fmt.Sprintf("[%d/%d] %s (embedded %d chunks, %dms)",
			r.activeFileIdx, r.totalFiles, ev.Path, ev.Chunks, ev.EmbedMS))

	case client.EventFileDone:
		// Leave the embedded line — file_done arrives so quickly that
		// rewriting it again is just flicker.

	case client.EventHeartbeat:
		if r.activeFile != "" {
			elapsed := time.Since(r.fileStart).Round(time.Second)
			r.statusLine(fmt.Sprintf("[%d/%d] %s · %s elapsed",
				r.activeFileIdx, r.totalFiles, r.activeFile, elapsed))
		}

	case client.EventFileError:
		r.endStatusLine()
		fmt.Fprintf(r.out, "  ! %s: %s\n", ev.Path, ev.Message)

	case client.EventBatchDone:
		r.endStatusLine()
		fmt.Fprintf(r.out, "  Processed %d/%d files (%d chunks)\n",
			ev.FilesProcessedTotal, r.totalFiles, ev.ChunksCreated)

	case client.EventError:
		if ev.Fatal {
			r.endStatusLine()
			fmt.Fprintf(r.out, "  ! server error: %s\n", ev.Message)
		}
	}
}

func (r *progressRenderer) renderLineByLine(ev client.ProgressEvent) {
	switch ev.Event {
	case client.EventFileStarted:
		idx := r.batchOffset + ev.FileIndex
		fmt.Fprintf(r.out, "  [%d/%d] %s\n", idx, r.totalFiles, ev.Path)
	case client.EventFileError:
		fmt.Fprintf(r.out, "  ! %s: %s\n", ev.Path, ev.Message)
	case client.EventBatchDone:
		fmt.Fprintf(r.out, "  Processed %d/%d files (%d chunks)\n",
			ev.FilesProcessedTotal, r.totalFiles, ev.ChunksCreated)
	case client.EventError:
		if ev.Fatal {
			fmt.Fprintf(r.out, "  ! server error: %s\n", ev.Message)
		}
	}
}

func (r *progressRenderer) renderQuiet(ev client.ProgressEvent) {
	switch ev.Event {
	case client.EventFileError:
		fmt.Fprintf(r.out, "  ! %s: %s\n", ev.Path, ev.Message)
	case client.EventBatchDone:
		fmt.Fprintf(r.out, "  Processed %d/%d files (%d chunks)\n",
			ev.FilesProcessedTotal, r.totalFiles, ev.ChunksCreated)
	case client.EventError:
		if ev.Fatal {
			fmt.Fprintf(r.out, "  ! server error: %s\n", ev.Message)
		}
	}
}

// statusLine clears the previous line (overwriting with spaces, then \r) and
// writes the new line without a trailing newline. Avoids ANSI escapes so it
// works in any terminal. Width is measured in runes — len() on a string with
// `…` (U+2026, 3 bytes) would over-pad and leave residue from the previous
// line at the right edge.
func (r *progressRenderer) statusLine(s string) {
	runes := utf8.RuneCountInString(s)
	if runes < r.lastLineRunes {
		// Pad with spaces to erase the longer previous text, then \r back
		// so the next write overwrites again.
		fmt.Fprintf(r.out, "\r%s", s+spaces(r.lastLineRunes-runes))
	} else {
		fmt.Fprintf(r.out, "\r%s", s)
	}
	r.lastLineRunes = runes
}

// endStatusLine writes a newline so subsequent output starts on a fresh line.
func (r *progressRenderer) endStatusLine() {
	if r.lastLineRunes > 0 {
		fmt.Fprintln(r.out)
		r.lastLineRunes = 0
	}
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
