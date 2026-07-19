package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/cli/internal/client"
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

// ---------------------------------------------------------------------------
// Mutation verbs: create / delete / add / remove / update.
// ---------------------------------------------------------------------------

// TestCreateWorkspace verifies `cix ws create <name> --description ...` POSTs
// the right body and renders the created id/name. The description flag is
// threaded through the global the same way cobra sets it in real use.
func TestCreateWorkspace(t *testing.T) {
	var gotBody map[string]any
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, http.StatusCreated, map[string]any{
				"id": "ws_new", "name": "platform", "description": "core repos",
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

	prev := wsDescription
	wsDescription = "core repos"
	t.Cleanup(func() { wsDescription = prev })

	out, err := captureOutput(func() error { return cmdCreateWorkspace(cli, []string{"platform"}) })
	if err != nil {
		t.Fatalf("cmdCreateWorkspace: %v", err)
	}
	if gotBody["name"] != "platform" {
		t.Errorf("expected name=platform in body, got: %v", gotBody)
	}
	if gotBody["description"] != "core repos" {
		t.Errorf("expected description in body, got: %v", gotBody)
	}
	if !strings.Contains(out, "created workspace platform") || !strings.Contains(out, "ws_new") {
		t.Errorf("expected created confirmation with id, got:\n%s", out)
	}
}

// TestCreateWorkspace_ArgCount pins the arity guard — create needs exactly
// one positional name; zero or many is a usage error (never a silent POST).
func TestCreateWorkspace_ArgCount(t *testing.T) {
	cli := &client.Client{}
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := cmdCreateWorkspace(cli, args); err == nil {
			t.Errorf("cmdCreateWorkspace(%v): expected arity error, got nil", args)
		}
	}
}

// TestDeleteWorkspace_WithYes confirms `--yes` skips the prompt and the
// resolved id is DELETEd. The 204 must be treated as success.
func TestDeleteWorkspace_WithYes(t *testing.T) {
	var deletedPath string
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces":
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/workspaces/ws_1":
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	prev := wsYes
	wsYes = true
	t.Cleanup(func() { wsYes = prev })

	out, err := captureOutput(func() error { return cmdDeleteWorkspace(cli, "platform") })
	if err != nil {
		t.Fatalf("cmdDeleteWorkspace: %v", err)
	}
	if deletedPath != "/api/v1/workspaces/ws_1" {
		t.Errorf("expected DELETE on resolved id ws_1, got %q", deletedPath)
	}
	if !strings.Contains(out, "deleted workspace platform") {
		t.Errorf("expected deletion confirmation, got:\n%s", out)
	}
}

// TestDeleteWorkspace_NonInteractiveRequiresYes locks the safety gate: with
// no TTY and no --yes, delete must refuse rather than block reading stdin.
// We force a non-interactive stdin (a pipe) so isInteractive() is
// deterministically false regardless of how the test harness is launched.
func TestDeleteWorkspace_NonInteractiveRequiresYes(t *testing.T) {
	withPipedStdin(t, "")
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
			return
		}
		// A DELETE reaching the server here would be a bug — the gate must
		// stop before any HTTP mutation.
		t.Errorf("unexpected request %s %s — delete gate should have aborted", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	prev := wsYes
	wsYes = false
	t.Cleanup(func() { wsYes = prev })

	_, err = captureOutput(func() error { return cmdDeleteWorkspace(cli, "platform") })
	if err == nil {
		t.Fatal("expected refusal error without --yes on non-interactive stdin")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should point at --yes, got: %v", err)
	}
}

// TestAddProjects_ResolvesAndLinks covers the happy path: an identifier is
// resolved to a path_hash via the live project list, then POSTed as
// {project_hash}. We exercise all three identifier forms in one run.
func TestAddProjects_ResolvesAndLinks(t *testing.T) {
	var linkedHashes []string
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{"path_hash": "a1b2c3d4e5f60718", "host_path": "github.com/owner/repo@main", "status": "indexed"},
				},
				"total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1/projects" && r.Method == http.MethodPost:
			var body struct {
				ProjectHash string `json:"project_hash"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			linkedHashes = append(linkedHashes, body.ProjectHash)
			writeJSON(w, http.StatusCreated, map[string]any{
				"workspace_id": "ws_1", "project_path": "github.com/owner/repo@main",
			})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	// Both the host_path form and the raw hash form must resolve to the
	// same path_hash and produce two link calls.
	out, err := captureOutput(func() error {
		return cmdAddProjects(cli, "platform", []string{"github.com/owner/repo@main", "a1b2c3d4e5f60718"})
	})
	if err != nil {
		t.Fatalf("cmdAddProjects: %v", err)
	}
	if len(linkedHashes) != 2 {
		t.Fatalf("expected 2 link calls, got %d (%v)", len(linkedHashes), linkedHashes)
	}
	for _, h := range linkedHashes {
		if h != "a1b2c3d4e5f60718" {
			t.Errorf("expected project_hash a1b2c3d4e5f60718, got %q", h)
		}
	}
	if strings.Count(out, "✓ added") != 2 {
		t.Errorf("expected two success lines, got:\n%s", out)
	}
}

// TestAddProjects_NotIndexedSurfacesError checks the 422 path: the server
// rejects a not-yet-indexed project and the CLI reports a per-project
// failure plus a non-nil aggregate error (non-zero exit).
func TestAddProjects_NotIndexedSurfacesError(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{"path_hash": "a1b2c3d4e5f60718", "host_path": "github.com/owner/repo@main", "status": "indexing"},
				},
				"total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1/projects" && r.Method == http.MethodPost:
			apiError(w, http.StatusUnprocessableEntity,
				"project is not yet indexed — wait for indexing to complete before linking")
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	_, err = captureOutput(func() error {
		return cmdAddProjects(cli, "platform", []string{"github.com/owner/repo@main"})
	})
	if err == nil {
		t.Fatal("expected aggregate error when a link fails")
	}
	if !strings.Contains(err.Error(), "failed to add") {
		t.Errorf("expected aggregate 'failed to add' error, got: %v", err)
	}
}

// TestAddProjects_UnknownProject asserts the local resolution guard: an
// identifier absent from the project list fails before any POST, with an
// actionable hint.
func TestAddProjects_UnknownProject(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces":
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case r.URL.Path == "/api/v1/projects":
			writeJSON(w, 200, map[string]any{"projects": []map[string]any{}, "total": 0})
		case strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/ws_1/projects"):
			t.Errorf("unexpected link POST for an unresolved project")
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	_, err = captureOutput(func() error {
		return cmdAddProjects(cli, "platform", []string{"github.com/nope/nope@main"})
	})
	if err == nil {
		t.Fatal("expected failure for an unknown project")
	}
}

// TestRemoveProjects_Unlinks confirms remove resolves the hash and issues a
// DELETE at the {id}/projects/{hash} path.
func TestRemoveProjects_Unlinks(t *testing.T) {
	var deletedPath string
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}},
				"total":      1,
			})
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{"path_hash": "a1b2c3d4e5f60718", "host_path": "github.com/owner/repo@main", "status": "indexed"},
				},
				"total": 1,
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/ws_1/projects/"):
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)

	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	out, err := captureOutput(func() error {
		return cmdRemoveProjects(cli, "platform", []string{"a1b2c3d4e5f60718"})
	})
	if err != nil {
		t.Fatalf("cmdRemoveProjects: %v", err)
	}
	if deletedPath != "/api/v1/workspaces/ws_1/projects/a1b2c3d4e5f60718" {
		t.Errorf("expected unlink DELETE on the hash path, got %q", deletedPath)
	}
	if !strings.Contains(out, "✓ removed") {
		t.Errorf("expected removal confirmation, got:\n%s", out)
	}
}

// TestResolveProjectHash exercises the three-tier identifier resolver
// directly (pure over a fixture list) so the priority order and the
// not-found error are pinned without HTTP.
func TestResolveProjectHash(t *testing.T) {
	projects := []client.Project{
		{PathHash: "a1b2c3d4e5f60718", HostPath: "github.com/owner/repo@main"},
		{PathHash: "7f3e2c1a0d4b5e69", HostPath: "/Users/me/local-proj"},
	}
	cases := []struct {
		in       string
		wantHash string
		wantErr  bool
	}{
		{"a1b2c3d4e5f60718", "a1b2c3d4e5f60718", false},           // raw hash
		{"github.com/owner/repo@main", "a1b2c3d4e5f60718", false}, // external host_path
		{"/Users/me/local-proj", "7f3e2c1a0d4b5e69", false},       // absolute host_path
		{"deadbeefdeadbeef", "", true},                            // hex but unknown
		{"nope", "", true},                                        // no match
	}
	for _, c := range cases {
		got, _, err := resolveProjectHash(projects, c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveProjectHash(%q): expected error, got hash=%q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveProjectHash(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.wantHash {
			t.Errorf("resolveProjectHash(%q) = %q, want %q", c.in, got, c.wantHash)
		}
	}
}

// TestResolveProjectHash_LocalDerived locks the local-project path, which a
// naive host_path string compare misses: the server stores a local project's
// host_path as the full "local:{machine_id}:{path}" identity key, so an
// absolute path must be resolved by re-deriving the path_hash (matching the
// server's key derivation), not by matching the bare path against host_path.
// This is the exact regression the E2E run surfaced.
func TestResolveProjectHash_LocalDerived(t *testing.T) {
	absPath := "/Users/me/some/local/project"
	derived := client.EncodeProjectPath(absPath) // sha1("local:<machine_id>:<abs>")
	projects := []client.Project{
		{PathHash: derived, HostPath: "local:machineabc:" + absPath},
	}
	got, hp, err := resolveProjectHash(projects, absPath)
	if err != nil {
		t.Fatalf("resolveProjectHash(%q): unexpected error: %v", absPath, err)
	}
	if got != derived {
		t.Errorf("resolveProjectHash(%q) = %q, want derived hash %q", absPath, got, derived)
	}
	if hp != "local:machineabc:"+absPath {
		t.Errorf("expected the stored host_path back, got %q", hp)
	}
}

// TestReadAffirmative pins the yes-parsing used by the delete prompt.
func TestReadAffirmative(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"yes\n", true}, {"Y\n", true}, {"YES\n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"nope\n", false},
	}
	for _, c := range cases {
		withPipedStdin(t, c.in)
		if got := readAffirmative(); got != c.want {
			t.Errorf("readAffirmative(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestCreateKeywordRouting proves `cix ws create <name>` routes to create
// (a POST) rather than being read as "describe the workspace named create".
func TestCreateKeywordRouting(t *testing.T) {
	posted := false
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			posted = true
			writeJSON(w, http.StatusCreated, map[string]any{"id": "ws_x", "name": "newws"})
			return
		}
		http.NotFound(w, r)
	})
	useAPI(t, srv)

	prev := wsDescription
	wsDescription = ""
	t.Cleanup(func() { wsDescription = prev })

	_, err := captureOutput(func() error { return runWorkspace(workspaceCmd, []string{"create", "newws"}) })
	if err != nil {
		t.Fatalf("runWorkspace create: %v", err)
	}
	if !posted {
		t.Error("expected `ws create newws` to POST a new workspace")
	}
}

// TestRenameWorkspace confirms `rename` PATCHes only the name (no description
// key) and renders the confirmation.
func TestRenameWorkspace(t *testing.T) {
	var gotBody map[string]any
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}}, "total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1" && r.Method == http.MethodPatch:
			json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, map[string]any{"id": "ws_1", "name": "platform-core"})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)
	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	out, err := captureOutput(func() error { return cmdRenameWorkspace(cli, "platform", "platform-core") })
	if err != nil {
		t.Fatalf("cmdRenameWorkspace: %v", err)
	}
	if gotBody["name"] != "platform-core" {
		t.Errorf("expected name=platform-core in PATCH body, got %v", gotBody)
	}
	if _, hasDesc := gotBody["description"]; hasDesc {
		t.Errorf("rename must not send a description key, got %v", gotBody)
	}
	if !strings.Contains(out, "renamed workspace to platform-core") {
		t.Errorf("expected rename confirmation, got:\n%s", out)
	}
}

// TestUpdateWorkspace_ClearsDescription is the key regression guard for the
// cmd.Flags().Changed semantics: `--description ""` must send an explicit
// empty description (clear it), NOT drop the field — and must not send a name
// key when only --description was given. A future refactor that switches to a
// zero-value check would silently break the clear-on-empty behavior.
func TestUpdateWorkspace_ClearsDescription(t *testing.T) {
	var raw []byte
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}}, "total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1" && r.Method == http.MethodPatch:
			raw, _ = io.ReadAll(r.Body)
			writeJSON(w, 200, map[string]any{"id": "ws_1", "name": "platform", "description": ""})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)
	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	setWSFlag(t, "description", "") // --description "" → Changed=true, clears

	_, err = captureOutput(func() error { return cmdUpdateWorkspace(workspaceCmd, cli, "platform") })
	if err != nil {
		t.Fatalf("cmdUpdateWorkspace: %v", err)
	}
	var body map[string]any
	if e := json.Unmarshal(raw, &body); e != nil {
		t.Fatalf("decode PATCH body: %v", e)
	}
	d, hasDesc := body["description"]
	if !hasDesc {
		t.Errorf("expected an explicit description key (clear), got %v", body)
	}
	if d != "" {
		t.Errorf("expected empty description, got %v", d)
	}
	if _, hasName := body["name"]; hasName {
		t.Errorf("did not expect a name key when only --description was set, got %v", body)
	}
}

// TestUpdateWorkspace_NoFlags: with neither flag changed, update errors before
// any HTTP call and names both flags.
func TestUpdateWorkspace_NoFlags(t *testing.T) {
	f := workspaceCmd.Flags()
	nc, dc := f.Lookup("name").Changed, f.Lookup("description").Changed
	f.Lookup("name").Changed = false
	f.Lookup("description").Changed = false
	t.Cleanup(func() { f.Lookup("name").Changed = nc; f.Lookup("description").Changed = dc })

	err := cmdUpdateWorkspace(workspaceCmd, &client.Client{}, "platform")
	if err == nil {
		t.Fatal("expected error when neither --name nor --description is given")
	}
	if !strings.Contains(err.Error(), "--name") || !strings.Contains(err.Error(), "--description") {
		t.Errorf("error should mention both flags, got: %v", err)
	}
}

// TestGuardVerbFlags locks the irrelevant-flag guard: --name (update-only) is
// rejected for create/delete but accepted for update.
func TestGuardVerbFlags(t *testing.T) {
	setWSFlag(t, "name", "x")
	if err := guardVerbFlags(workspaceCmd, "create"); err == nil {
		t.Error("expected --name rejected for create")
	} else if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error should mention --name, got: %v", err)
	}
	if err := guardVerbFlags(workspaceCmd, "delete"); err == nil {
		t.Error("expected --name rejected for delete")
	}
	if err := guardVerbFlags(workspaceCmd, "update"); err != nil {
		t.Errorf("--name must be valid for update, got: %v", err)
	}
}

// TestCreateWorkspace_RejectsReservedName pins that `list`/`create` (any case)
// are refused as workspace names — they'd be unaddressable by the name-first
// grammar.
func TestCreateWorkspace_RejectsReservedName(t *testing.T) {
	cli := &client.Client{}
	for _, name := range []string{"list", "create", "LIST", "Create"} {
		if err := cmdCreateWorkspace(cli, []string{name}); err == nil {
			t.Errorf("cmdCreateWorkspace(%q): expected reserved-name error, got nil", name)
		}
	}
}

// TestAddProjects_JSON verifies --json emits a machine-readable summary on
// stdout (and no ✓ lines) for the add path.
func TestAddProjects_JSON(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}}, "total": 1,
			})
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"projects": []map[string]any{
					{"path_hash": "a1b2c3d4e5f60718", "host_path": "github.com/owner/repo@main", "status": "indexed"},
				}, "total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1/projects" && r.Method == http.MethodPost:
			writeJSON(w, http.StatusCreated, map[string]any{"workspace_id": "ws_1"})
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)
	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	prev := wsJSON
	wsJSON = true
	t.Cleanup(func() { wsJSON = prev })

	out, err := captureOutput(func() error {
		return cmdAddProjects(cli, "platform", []string{"github.com/owner/repo@main"})
	})
	if err != nil {
		t.Fatalf("cmdAddProjects: %v", err)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("JSON mode must not print ✓ lines, got:\n%s", out)
	}
	var parsed struct {
		Workspace string `json:"workspace"`
		Failed    int    `json:"failed"`
		Results   []struct {
			HostPath string `json:"host_path"`
			Status   string `json:"status"`
		} `json:"results"`
	}
	if e := json.Unmarshal([]byte(out), &parsed); e != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", e, out)
	}
	if parsed.Failed != 0 || len(parsed.Results) != 1 || parsed.Results[0].Status != "added" {
		t.Errorf("unexpected JSON result: %+v", parsed)
	}
}

// TestDeleteWorkspace_JSON verifies --json emits a structured deletion record.
func TestDeleteWorkspace_JSON(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/workspaces" && r.Method == http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]any{{"id": "ws_1", "name": "platform"}}, "total": 1,
			})
		case r.URL.Path == "/api/v1/workspaces/ws_1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	useAPI(t, srv)
	cli, err := getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	prevJSON, prevYes := wsJSON, wsYes
	wsJSON, wsYes = true, true
	t.Cleanup(func() { wsJSON, wsYes = prevJSON, prevYes })

	out, err := captureOutput(func() error { return cmdDeleteWorkspace(cli, "platform") })
	if err != nil {
		t.Fatalf("cmdDeleteWorkspace: %v", err)
	}
	var parsed struct {
		Deleted   bool   `json:"deleted"`
		Workspace string `json:"workspace"`
	}
	if e := json.Unmarshal([]byte(out), &parsed); e != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", e, out)
	}
	if !parsed.Deleted || parsed.Workspace != "platform" {
		t.Errorf("unexpected JSON: %+v", parsed)
	}
}

// setWSFlag sets a workspaceCmd flag (marking it Changed) for the duration of
// the test, restoring both the value and the Changed bit afterward so flag
// state never leaks across tests.
func setWSFlag(t *testing.T, name, value string) {
	t.Helper()
	f := workspaceCmd.Flags()
	lk := f.Lookup(name)
	if lk == nil {
		t.Fatalf("unknown flag %q", name)
	}
	prevChanged := lk.Changed
	prevVal := lk.Value.String()
	if err := f.Set(name, value); err != nil {
		t.Fatalf("set flag %s: %v", name, err)
	}
	t.Cleanup(func() {
		f.Set(name, prevVal)
		lk.Changed = prevChanged
	})
}

// withPipedStdin swaps os.Stdin for the read end of a pipe pre-filled with
// input, making isInteractive() deterministically false and feeding
// readAffirmative(). Restored on cleanup.
func withPipedStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	go func() {
		io.WriteString(w, input)
		w.Close()
	}()
	t.Cleanup(func() { os.Stdin = old })
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
