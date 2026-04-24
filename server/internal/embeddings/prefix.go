package embeddings

import "strings"

// QueryPrefixes mirrors api/app/services/embeddings.py:18-24. These are the
// asymmetric-retrieval query prefixes each model expects — passages are
// embedded unchanged, queries are embedded with the prefix prepended.
//
// Keep this map string-for-string identical to the Python dict. The parity gate
// depends on the prefix being literally the same bytes sent to the model.
var QueryPrefixes = map[string]string{
	"nomic-ai/CodeRankEmbed":              "Represent this query for searching relevant code: ",
	"nomic-ai/nomic-embed-text-v1.5":      "search_query: ",
	"BAAI/bge-base-en-v1.5":               "Represent this sentence for searching relevant passages: ",
	"BAAI/bge-large-en-v1.5":              "Represent this sentence for searching relevant passages: ",
	"awhiteside/CodeRankEmbed-Q8_0-GGUF":  "Represent this query for searching relevant code: ",
}

// ResolveQueryPrefix returns the prefix string to prepend to queries for the
// named model. Exact-match wins; otherwise falls back to substring matching on
// the lowercased name, matching api/app/services/embeddings.py:27-39.
//
// An empty string is returned when no rule matches — callers must not assume
// the model supports asymmetric retrieval.
func ResolveQueryPrefix(model string) string {
	if p, ok := QueryPrefixes[model]; ok {
		return p
	}
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "coderankembed"):
		return QueryPrefixes["nomic-ai/CodeRankEmbed"]
	case strings.Contains(lower, "nomic-embed-text"):
		return QueryPrefixes["nomic-ai/nomic-embed-text-v1.5"]
	case strings.Contains(lower, "bge-base"):
		return QueryPrefixes["BAAI/bge-base-en-v1.5"]
	case strings.Contains(lower, "bge-large"):
		return QueryPrefixes["BAAI/bge-large-en-v1.5"]
	}
	return ""
}
