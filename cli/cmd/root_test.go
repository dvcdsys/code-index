package cmd

import (
	"net/http"
	"testing"
)

func TestFindProjectRoot(t *testing.T) {
	root := "/home/user/myproject"
	sub := root + "/src/api"
	deep := root + "/src/api/handlers/auth"
	other := "/home/user/other"

	tests := []struct {
		name      string
		projects  []string // registered projects
		candidate string
		want      string
	}{
		{
			name:      "exact match",
			projects:  []string{root},
			candidate: root,
			want:      root,
		},
		{
			name:      "direct subdirectory",
			projects:  []string{root},
			candidate: sub,
			want:      root,
		},
		{
			name:      "deep subdirectory",
			projects:  []string{root},
			candidate: deep,
			want:      root,
		},
		{
			name:      "no match returns original",
			projects:  []string{root},
			candidate: other,
			want:      other,
		},
		{
			name:      "empty project list returns original",
			projects:  []string{},
			candidate: sub,
			want:      sub,
		},
		{
			name:      "picks longest matching prefix",
			projects:  []string{root, root + "/src"},
			candidate: deep,
			want:      root + "/src",
		},
		{
			name:      "no false prefix match (similar path prefix)",
			projects:  []string{"/home/user/myproj"},
			candidate: "/home/user/myproject/src",
			want:      "/home/user/myproject/src",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := mockServer(t, listProjectsHandler(tc.projects))
			useAPI(t, srv)

			c, _ := getClient()
			got := findProjectRoot(tc.candidate, c)
			if got != tc.want {
				t.Errorf("findProjectRoot(%q) = %q, want %q", tc.candidate, got, tc.want)
			}
		})
	}
}

func TestFindProjectRoot_APIError(t *testing.T) {
	// When ListProjects fails, the original path should be returned unchanged.
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "server error")
	})
	useAPI(t, srv)

	candidate := "/some/path"
	c, _ := getClient()
	got := findProjectRoot(candidate, c)
	if got != candidate {
		t.Errorf("expected fallback to %q, got %q", candidate, got)
	}
}

func TestFormatStatus(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"indexed", "✓ Indexed"},
		{"indexing", "⏳ Indexing"},
		{"created", "○ Created (not indexed)"},
		{"error", "✗ Error"},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		if got := formatStatus(tc.in); got != tc.want {
			t.Errorf("formatStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"indexed", "✓"},
		{"indexing", "⏳"},
		{"created", "○"},
		{"error", "✗"},
		{"other", "?"},
	}
	for _, tc := range tests {
		if got := getStatusIcon(tc.in); got != tc.want {
			t.Errorf("getStatusIcon(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetClient_ReturnsClient(t *testing.T) {
	// Verify getClient succeeds when apiURL and apiKey are set via flags.
	prev, prevKey := apiURL, apiKey
	apiURL = "http://localhost:19999"
	apiKey = "test-key"
	defer func() { apiURL = prev; apiKey = prevKey }()

	c, err := getClient()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}
