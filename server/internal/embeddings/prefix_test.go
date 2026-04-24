package embeddings

import "testing"

func TestResolveQueryPrefix(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{
			name:  "exact match default model",
			model: "awhiteside/CodeRankEmbed-Q8_0-GGUF",
			want:  "Represent this query for searching relevant code: ",
		},
		{
			name:  "exact match nomic coderankembed",
			model: "nomic-ai/CodeRankEmbed",
			want:  "Represent this query for searching relevant code: ",
		},
		{
			name:  "exact match nomic-embed-text v1.5",
			model: "nomic-ai/nomic-embed-text-v1.5",
			want:  "search_query: ",
		},
		{
			name:  "exact match bge-base",
			model: "BAAI/bge-base-en-v1.5",
			want:  "Represent this sentence for searching relevant passages: ",
		},
		{
			name:  "exact match bge-large",
			model: "BAAI/bge-large-en-v1.5",
			want:  "Represent this sentence for searching relevant passages: ",
		},
		{
			name:  "substring fallback coderankembed via custom repo",
			model: "someuser/coderankembed-fp16",
			want:  "Represent this query for searching relevant code: ",
		},
		{
			name:  "substring fallback nomic-embed-text",
			model: "foo/nomic-embed-text-v2",
			want:  "search_query: ",
		},
		{
			name:  "substring fallback bge-base uppercase",
			model: "Other/BGE-Base-en-v2",
			want:  "Represent this sentence for searching relevant passages: ",
		},
		{
			name:  "substring fallback bge-large",
			model: "alt/bge-large-tuned",
			want:  "Represent this sentence for searching relevant passages: ",
		},
		{
			name:  "no match returns empty",
			model: "intfloat/e5-base-v2",
			want:  "",
		},
		{
			name:  "empty model returns empty",
			model: "",
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveQueryPrefix(tc.model)
			if got != tc.want {
				t.Errorf("ResolveQueryPrefix(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
