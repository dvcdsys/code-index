package provider

import "testing"

func TestStorageSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"voyage:voyage-code-3:2048:float", "voyage_voyage_code_3_2048_float"},
		{"ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF", "ollama_awhiteside_coderankembed_q8_0_gguf"},
		{"openai:text-embedding-3-large:256", "openai_text_embedding_3_large_256"},
		{"OpenAI:Foo.Bar", "openai_foo_bar"},     // mixed case + dot
		{"a b", "a_b"},                           // space
		{"", ""},                                 // empty
		{"already_safe_123", "already_safe_123"}, // identity for safe chars
	}
	for _, tc := range cases {
		if got := StorageSlug(tc.in); got != tc.want {
			t.Errorf("StorageSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStorageSlugIdempotent ensures slugging an already-slugged string is
// a no-op (the chroma migration relies on this so re-running never double-
// transforms a name).
func TestStorageSlugIdempotent(t *testing.T) {
	for _, in := range []string{
		"voyage:voyage-code-3:2048:float",
		"ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF",
	} {
		once := StorageSlug(in)
		twice := StorageSlug(once)
		if once != twice {
			t.Errorf("StorageSlug not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}
