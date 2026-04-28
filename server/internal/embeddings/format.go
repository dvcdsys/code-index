package embeddings

import (
	"strings"

	"github.com/dvcdsys/code-index/server/internal/chunker"
)

// FormatChunkForEmbedding builds the text passed to the embedder for a chunk.
// It optionally prepends a natural-language preamble carrying the relative
// path, language, and symbol kind+name. Code-trained embedders interpret this
// preamble as docstring-style context — empirically it improves retrieval for
// path-aware queries (e.g. "server search handler") because file paths
// contribute high-signal tokens that bare chunk content lacks.
//
// Off-recipe for CodeRankEmbed (whose passage side was trained on raw code),
// but the cost is a few dozen extra tokens per chunk and the gain on this
// repo's "main entry point server" type queries is large enough to be worth
// the trade. Switching this format on or off requires a full reindex —
// vectors are not interchangeable between formats.
//
// When relPath is empty (or includePath=false), the function falls back to
// the legacy "<chunk_type>: <content>" prefix that the Python indexer used,
// preserving parity for projects that have not yet reindexed.
func FormatChunkForEmbedding(c chunker.Chunk, relPath string, includePath bool) string {
	if !includePath || relPath == "" {
		return c.ChunkType + ": " + c.Content
	}

	var sb strings.Builder
	sb.Grow(len(relPath) + len(c.Content) + 64)

	sb.WriteString("File: ")
	sb.WriteString(relPath)
	sb.WriteByte('\n')

	if c.Language != "" {
		sb.WriteString("Language: ")
		sb.WriteString(c.Language)
		sb.WriteByte('\n')
	}

	// Symbol metadata is only included for nameable chunks. "module" / "block"
	// chunks have no symbol and would just add noise. The chunker stores
	// SymbolName as a *string; nil means "no symbol".
	if c.SymbolName != nil && *c.SymbolName != "" {
		switch c.ChunkType {
		case "function", "class", "method", "type":
			sb.WriteString(c.ChunkType)
			sb.WriteString(": ")
			sb.WriteString(*c.SymbolName)
			sb.WriteByte('\n')
		}
	}

	sb.WriteByte('\n')
	sb.WriteString(c.Content)
	return sb.String()
}
