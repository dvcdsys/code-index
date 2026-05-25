package cmd

import (
	"net/http"
	"strings"
	"testing"
)

// TestListWorkspaceProjects_DecodesPayload locks the acceptance from
// docs/code-review-workspaces-link-local-projects.md (Fix #1, line 284):
// after the rewrite, `cix ws <name> list` must return 200 and render a
// readable list with status badges. We also assert the absence of the
// literal "@undefined" — the regression that broke the dashboard side
// of this contract per Fix #2.
func TestListWorkspaceProjects_DecodesPayload(t *testing.T) {
	srv := mockServer(t, defaultWorkspaceHandler())
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	prevVerbose := wsVerbose
	wsVerbose = true
	t.Cleanup(func() { wsVerbose = prevVerbose })

	out, err := captureOutput(func() error { return cmdListRepos(cli, "platform") })
	if err != nil {
		t.Fatalf("cmdListRepos: %v", err)
	}

	// Status badges per the new enum.
	if !strings.Contains(out, "✓ indexed") {
		t.Errorf("expected '✓ indexed' badge, got:\n%s", out)
	}
	if !strings.Contains(out, "… indexing") {
		t.Errorf("expected '… indexing' badge, got:\n%s", out)
	}

	// Host-paths render directly — github form already carries @branch.
	if !strings.Contains(out, "github.com/owner/repo@main") {
		t.Errorf("expected github host_path with @branch, got:\n%s", out)
	}
	if !strings.Contains(out, "/Users/me/local-proj") {
		t.Errorf("expected local host_path, got:\n%s", out)
	}

	// Verbose extras for the indexed row.
	if !strings.Contains(out, "path_hash: a1b2c3d4e5f60718") {
		t.Errorf("expected path_hash in verbose output, got:\n%s", out)
	}
	if !strings.Contains(out, "last indexed: 2026-05-14T12:30:45Z") {
		t.Errorf("expected RFC3339 last_indexed in verbose output, got:\n%s", out)
	}
	if !strings.Contains(out, "languages: go, typescript") {
		t.Errorf("expected languages line for indexed row, got:\n%s", out)
	}

	// Regression canary — Fix #2 dashboard bug rendered the literal
	// "@undefined" because branch came from a missing field. The CLI
	// equivalent must never print that.
	if strings.Contains(out, "@undefined") || strings.Contains(out, "undefined") {
		t.Errorf("unexpected 'undefined' in output:\n%s", out)
	}
}

// TestListWorkspaces_VerboseProjectCount covers the silent-fail path
// that broke `cix ws list -v` — it used to swallow 404s from the deleted
// /repos endpoint and just omit the count row. After the fix the verbose
// row must reappear with the new "projects" terminology.
func TestListWorkspaces_VerboseProjectCount(t *testing.T) {
	srv := mockServer(t, defaultWorkspaceHandler())
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	prevVerbose := wsVerbose
	wsVerbose = true
	t.Cleanup(func() { wsVerbose = prevVerbose })

	out, err := captureOutput(func() error { return cmdListWorkspaces(cli) })
	if err != nil {
		t.Fatalf("cmdListWorkspaces: %v", err)
	}

	if !strings.Contains(out, "2 projects (1 indexed)") {
		t.Errorf("expected '2 projects (1 indexed)' verbose count, got:\n%s", out)
	}
	// Sanity: the old wording must not leak back.
	if strings.Contains(out, "repos (") {
		t.Errorf("unexpected old 'repos (...)' wording in output:\n%s", out)
	}
}

// TestListWorkspaceProjects_ServiceUnavailable locks in the
// CIX_WORKSPACES_ENABLED=false → 503 path. The CLI must surface a
// helpful error rather than crash or hang.
func TestListWorkspaceProjects_ServiceUnavailable(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces":
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case "/api/v1/workspaces/ws_1/projects":
			apiError(w, http.StatusServiceUnavailable,
				"workspaces feature is disabled (set CIX_WORKSPACES_ENABLED=true and restart)")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	_, err = captureOutput(func() error { return cmdListRepos(cli, "platform") })
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected error to mention 503 + 'disabled', got: %v", err)
	}
}

// TestDescribeWorkspace_ByCaseInsensitiveName exercises the
// describe path that lives separately from `resolveWorkspaceID` (it has
// its own inline name-match loop) and confirms mixed-case lookup works.
func TestDescribeWorkspace_ByCaseInsensitiveName(t *testing.T) {
	srv := mockServer(t, defaultWorkspaceHandler())
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	out, err := captureOutput(func() error { return cmdDescribeWorkspace(cli, "PLATFORM") })
	if err != nil {
		t.Fatalf("cmdDescribeWorkspace: %v", err)
	}

	if !strings.Contains(out, "Workspace: platform") {
		t.Errorf("expected workspace header, got:\n%s", out)
	}
	if !strings.Contains(out, "projects: 2 (1 indexed)") {
		t.Errorf("expected per-workspace project count line, got:\n%s", out)
	}
	if !strings.Contains(out, "github.com/owner/repo@main") {
		t.Errorf("expected indexed project's host_path in describe output, got:\n%s", out)
	}
	if !strings.Contains(out, "path_hash: a1b2c3d4e5f60718") {
		t.Errorf("expected path_hash in describe output, got:\n%s", out)
	}
}

// TestListWorkspaces_ParsesEmpty pins the empty-server response path —
// the CLI must handle `{"workspaces": [], "total": 0}` cleanly: no
// error, no spurious lines on stdout, and (silently here, on stderr in
// real use) an operator-friendly hint pointing at the dashboard. Fix #17
// minimum #1.
func TestListWorkspaces_ParsesEmpty(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{},
				"total":      0,
			})
			return
		}
		http.NotFound(w, r)
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	out, err := captureOutput(func() error { return cmdListWorkspaces(cli) })
	if err != nil {
		t.Fatalf("cmdListWorkspaces on empty list: %v", err)
	}
	// captureOutput only watches stdout; the "no workspaces — create one
	// at …" hint goes to stderr in the real binary. Stdout must be empty
	// so a future regression that accidentally prints a header row (or a
	// stray "0 workspaces" line) trips this assertion.
	if out != "" {
		t.Errorf("expected empty stdout for 0 workspaces, got: %q", out)
	}
}

// TestProjectStatusBadge — exhaustive per-status formatting check for
// the two badge helpers. Fix #17 minimum #2: a future renumber of the
// status enum (e.g. dropping 'created' or adding 'archived') must trip
// at least one of these table rows. Direct unit test bypasses the HTTP
// harness — the two functions are pure mappings.
func TestProjectStatusBadge(t *testing.T) {
	cases := []struct {
		in    string
		long  string
		short string
	}{
		{"indexed", "✓ indexed", "✓"},
		{"indexing", "… indexing", "…"},
		{"created", "… created", "…"},
		{"error", "✗ error", "✗"},
		// Default-arm coverage: unknown future statuses must surface
		// verbatim (long) and degrade to the "still working" glyph
		// (short) rather than crash or panic. This protects forward
		// compatibility — the CLI should render whatever the server
		// returns, not gate on the enum.
		{"archived", "archived", "…"},
	}
	for _, c := range cases {
		if got := projectStatusBadge(c.in); got != c.long {
			t.Errorf("projectStatusBadge(%q) = %q, want %q", c.in, got, c.long)
		}
		if got := projectStatusBadgeShort(c.in); got != c.short {
			t.Errorf("projectStatusBadgeShort(%q) = %q, want %q", c.in, got, c.short)
		}
	}
}

// TestResolveWorkspaceID_ByName covers Fix #17 minimum #3. The shared
// resolver supports three ways to address a workspace: exact ID, exact
// name (case-sensitive), and case-insensitive name match. Unknown
// identifiers must return an error mentioning the input so the user
// can correct the typo. Distinct from
// TestDescribeWorkspace_ByCaseInsensitiveName, which exercises the
// describe-command's inline name-match loop — this one hits the
// resolveWorkspaceID function used by `cix ws <name> list/repos`.
func TestResolveWorkspaceID_ByName(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{
					{"id": "ws_alpha", "name": "platform"},
					{"id": "ws_beta", "name": "ML-Pipeline"},
				},
				"total": 2,
			})
			return
		}
		http.NotFound(w, r)
	})
	useAPI(t, srv)
	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	cases := []struct {
		in      string
		wantID  string
		wantErr bool
	}{
		{"platform", "ws_alpha", false},   // exact name match
		{"PLATFORM", "ws_alpha", false},   // upper-case name match
		{"PlatForm", "ws_alpha", false},   // mixed-case name match
		{"ml-pipeline", "ws_beta", false}, // case-insensitive on hyphenated name
		{"ML-PIPELINE", "ws_beta", false}, // upper-case variant
		{"ws_alpha", "ws_alpha", false},   // exact ID match
		{"nonexistent", "", true},         // not found → error
	}
	for _, c := range cases {
		got, err := resolveWorkspaceID(cli, c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveWorkspaceID(%q): expected error, got id=%q", c.in, got)
				continue
			}
			if !strings.Contains(err.Error(), c.in) {
				t.Errorf("resolveWorkspaceID(%q): error should mention input, got: %v", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveWorkspaceID(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.wantID {
			t.Errorf("resolveWorkspaceID(%q) = %q, want %q", c.in, got, c.wantID)
		}
	}
}

// defaultWorkspaceHandler returns the standard 2-project fixture used
// by every test in this file. Factored out to avoid copy-pasting the
// JSON literal across handlers.
func defaultWorkspaceHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces":
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{
					{"id": "ws_1", "name": "platform", "description": "core platform repos"},
				},
				"total": 1,
			})
		case "/api/v1/workspaces/ws_1/projects":
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{
						"added_at": "2026-05-10T08:15:00Z",
						"project": map[string]any{
							"path_hash":       "a1b2c3d4e5f60718",
							"host_path":       "github.com/owner/repo@main",
							"container_path":  "/code/owner/repo",
							"languages":       []string{"go", "typescript"},
							"settings":        map[string]any{"exclude_patterns": []string{}, "max_file_size": 524288},
							"stats":           map[string]any{"total_files": 50, "indexed_files": 50, "total_chunks": 200, "total_symbols": 30},
							"status":          "indexed",
							"created_at":      "2026-05-01T00:00:00Z",
							"updated_at":      "2026-05-14T12:30:45Z",
							"last_indexed_at": "2026-05-14T12:30:45Z",
						},
					},
					{
						"added_at": "2026-05-11T09:00:00Z",
						"project": map[string]any{
							"path_hash":       "7f3e2c1a0d4b5e69",
							"host_path":       "/Users/me/local-proj",
							"container_path":  "/Users/me/local-proj",
							"languages":       []string{},
							"settings":        map[string]any{"exclude_patterns": []string{}, "max_file_size": 524288},
							"stats":           map[string]any{"total_files": 0, "indexed_files": 0, "total_chunks": 0, "total_symbols": 0},
							"status":          "indexing",
							"created_at":      "2026-05-11T08:55:00Z",
							"updated_at":      "2026-05-11T09:00:00Z",
							"last_indexed_at": nil,
						},
					},
				},
				"total": 2,
			})
		default:
			http.NotFound(w, r)
		}
	}
}
