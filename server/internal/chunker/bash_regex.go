// Package chunker — regex-based bash function extractor.
//
// Used as a fallback when tree-sitter-bash hits a parse pathology (see
// parseBudget in chunker.go). Tree-sitter would have given us better symbol
// data, but on a 7KB install.sh-style script its parser can spend 30 seconds
// on catastrophic backtracking. The regex extractor below recognises the
// three common bash function forms and finds each function's closing brace
// with a small state machine that handles strings, comments, and heredocs.
//
// Output schema matches chunkWithTreesitter so the indexer's downstream code
// (DB upserts, vector embeddings) doesn't need to special-case bash.
//
// Limitations vs full tree-sitter parse:
//   - No reference extraction (returns nil refs).
//   - Functions with a `{` on a line *separate* from the opener (`name()` on
//     one line, `{` on the next) are not matched. That form is legal in
//     bash but rare in practice; falls back to sliding-window for those.
//   - Comments containing `{`/`}` inside strings can confuse the brace
//     counter on adversarial inputs; bounded by maxBashFuncLines so a
//     malformed function never absorbs the whole file.

package chunker

import (
	"regexp"
	"strings"
)

// posixFuncRE matches the POSIX-shell style: `name() { ...`.
// Captures group 1 = function name. The trailing `{` must be on the same line.
var posixFuncRE = regexp.MustCompile(
	`^[[:space:]]*([A-Za-z_][A-Za-z0-9_:.-]*)[[:space:]]*\(\)[[:space:]]*\{`)

// bashFuncRE matches the bash-keyword style: `function name [()] { ...`.
// Captures group 1 = function name.
var bashFuncRE = regexp.MustCompile(
	`^[[:space:]]*function[[:space:]]+([A-Za-z_][A-Za-z0-9_:.-]*)(?:[[:space:]]*\(\))?[[:space:]]*\{`)

// maxBashFuncLines caps how far we'll scan for a function's closing `}`.
// Real-world bash functions rarely exceed ~200 lines. The cap protects
// against pathological inputs where the brace counter goes off-track —
// instead of consuming the whole file as one function, we stop and let the
// caller decide what to do (typically: keep a partial chunk, fall back
// to sliding-window for the remainder).
const maxBashFuncLines = 500

// bashRegexChunks extracts function-level chunks from bash source via regex.
// Returns nil when no functions were found, signalling the caller to fall
// through to sliding-window. Always returns nil refs (the regex doesn't
// track identifier usage).
func bashRegexChunks(filePath, content string) []Chunk {
	lines := splitLines(content)
	if len(lines) == 0 {
		return nil
	}

	var chunks []Chunk
	covered := make([]bool, len(lines))

	i := 0
	for i < len(lines) {
		line := lines[i]
		var name string
		if m := posixFuncRE.FindStringSubmatch(line); m != nil {
			name = m[1]
		} else if m := bashFuncRE.FindStringSubmatch(line); m != nil {
			name = m[1]
		}
		if name == "" {
			i++
			continue
		}

		endIdx, ok := scanBashFuncEnd(lines, i)
		if !ok {
			// Couldn't find balanced close within maxBashFuncLines.
			// Skip this opener — don't emit a wildly oversized chunk.
			i++
			continue
		}

		startLine := i + 1 // 1-based
		endLine := endIdx + 1
		body := joinLines(lines[i : endIdx+1])
		// Signature = the opener line trimmed.
		sigStr := trimSpace(line)
		nameCopy := name

		chunks = append(chunks, Chunk{
			Content:         body,
			ChunkType:       "function",
			FilePath:        filePath,
			StartLine:       startLine,
			EndLine:         endLine,
			Language:        "bash",
			SymbolName:      &nameCopy,
			SymbolSignature: &sigStr,
		})
		for k := i; k <= endIdx && k < len(covered); k++ {
			covered[k] = true
		}
		i = endIdx + 1
	}

	if len(chunks) == 0 {
		return nil
	}

	// Fill the gaps between functions with `module` chunks so the file's
	// non-function content (top-level commands, comments, set -e, etc.)
	// still gets indexed for full-text/semantic search.
	chunks = appendBashGaps(chunks, lines, covered, filePath)
	return chunks
}

// appendBashGaps adds module-type chunks for line ranges not covered by any
// function. Mirrors the gap-filling logic chunkWithTreesitter applies for
// tree-sitter chunks. Returns chunks sorted by StartLine.
func appendBashGaps(chunks []Chunk, lines []string, covered []bool, filePath string) []Chunk {
	gapStart := -1
	for i := 0; i <= len(lines); i++ {
		uncovered := i < len(lines) && !covered[i]
		if uncovered && gapStart < 0 {
			gapStart = i
		}
		if !uncovered && gapStart >= 0 {
			gapEnd := i - 1
			content := joinLines(lines[gapStart : gapEnd+1])
			if trimSpace(content) != "" {
				chunks = append(chunks, Chunk{
					Content:   content,
					ChunkType: "module",
					FilePath:  filePath,
					StartLine: gapStart + 1,
					EndLine:   gapEnd + 1,
					Language:  "bash",
				})
			}
			gapStart = -1
		}
	}
	// Sort by StartLine so consumers see a stable order.
	insertSortByStartLine(chunks)
	return chunks
}

func insertSortByStartLine(chunks []Chunk) {
	for i := 1; i < len(chunks); i++ {
		j := i
		for j > 0 && chunks[j].StartLine < chunks[j-1].StartLine {
			chunks[j], chunks[j-1] = chunks[j-1], chunks[j]
			j--
		}
	}
}

// scanBashFuncEnd walks forward from startLineIdx (the opener line, which
// already contains the first `{`) and returns the 0-based line index of the
// matching close `}` and ok=true. ok=false means we couldn't find a balance
// within maxBashFuncLines or hit EOF first.
//
// State machine handles:
//   - Single-quoted strings ('...') — literal, no escapes
//   - Double-quoted strings ("...") — `\"` is escaped quote, `\\` is escaped backslash
//   - `# ... EOL` comments — but skipping `$#`, `${#var}`, `$(( # ...))` etc. heuristically
//   - Heredocs (<<DELIM, <<-DELIM, <<"DELIM", <<'DELIM') — body skipped until DELIM line
//   - Here-strings (<<<word) — single-line, no special handling needed
func scanBashFuncEnd(lines []string, startLineIdx int) (int, bool) {
	depth := 0
	inSingleStr := false
	inDoubleStr := false
	inHeredoc := false
	heredocDelim := ""
	heredocStripTabs := false

	maxIdx := startLineIdx + maxBashFuncLines
	if maxIdx > len(lines) {
		maxIdx = len(lines)
	}

	for li := startLineIdx; li < maxIdx; li++ {
		line := lines[li]

		if inHeredoc {
			candidate := line
			if heredocStripTabs {
				candidate = strings.TrimLeft(line, "\t")
			}
			if candidate == heredocDelim {
				inHeredoc = false
				heredocDelim = ""
				heredocStripTabs = false
			}
			continue
		}

		i := 0
		for i < len(line) {
			c := line[i]

			if inSingleStr {
				if c == '\'' {
					inSingleStr = false
				}
				i++
				continue
			}
			if inDoubleStr {
				if c == '\\' && i+1 < len(line) {
					// Skip the escaped char (handles `\"`, `\\`, etc.).
					i += 2
					continue
				}
				if c == '"' {
					inDoubleStr = false
				}
				i++
				continue
			}

			// Comment — `#` starts a line comment unless it follows `$` (`$#`,
			// argument count) or `{`/`(` (`${#var}`, `$((# expr ))`). We
			// skip the comment if `#` is at start of line / after whitespace
			// or after a token-ending char.
			if c == '#' {
				prev := byte(' ')
				if i > 0 {
					prev = line[i-1]
				}
				if prev == ' ' || prev == '\t' || prev == ';' || prev == '|' ||
					prev == '&' || prev == '(' || i == 0 {
					break // rest of line is comment
				}
			}

			// Heredoc / here-string
			if c == '<' && i+1 < len(line) && line[i+1] == '<' {
				// `<<<` is here-string (single-line) — skip the marker
				if i+2 < len(line) && line[i+2] == '<' {
					i += 3
					continue
				}
				// `<<` or `<<-`
				j := i + 2
				stripTabs := false
				if j < len(line) && line[j] == '-' {
					stripTabs = true
					j++
				}
				// Skip leading whitespace before delimiter
				for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
					j++
				}
				delim, after := readHeredocDelim(line, j)
				if delim != "" {
					inHeredoc = true
					heredocDelim = delim
					heredocStripTabs = stripTabs
					// Resume after the delimiter on this line — there may
					// be more code on the opener line (e.g. `cmd <<EOF | tee log`)
					i = after
					continue
				}
				// Fallthrough — `<<` not followed by a delimiter (probably
				// arithmetic shift). Treat as two `<` chars and move on.
			}

			switch c {
			case '\'':
				inSingleStr = true
			case '"':
				inDoubleStr = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return li, true
				}
			}
			i++
		}
	}
	return 0, false
}

// readHeredocDelim parses the heredoc delimiter starting at `start` in line.
// Handles bare (`EOF`), single-quoted (`'EOF'`), and double-quoted (`"EOF"`)
// forms. Returns the delimiter and the position after it. delim="" when no
// valid delimiter was found.
func readHeredocDelim(line string, start int) (string, int) {
	if start >= len(line) {
		return "", start
	}
	q := line[start]
	if q == '\'' || q == '"' {
		end := strings.IndexByte(line[start+1:], q)
		if end < 0 {
			return "", start
		}
		return line[start+1 : start+1+end], start + 1 + end + 1
	}
	j := start
	for j < len(line) && (isBashIdentByte(line[j])) {
		j++
	}
	if j == start {
		return "", start
	}
	return line[start:j], j
}

func isBashIdentByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '-'
}
