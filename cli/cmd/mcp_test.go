package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRegisterCixTools_SchemaReflection guards that every tool's input schema
// reflects cleanly — in particular the optional *float64 min_score fields on
// cix_search / cix_workspace_search. A bad schema panics here at AddTool time.
func TestRegisterCixTools_SchemaReflection(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cix", Version: "test"}, nil)
	registerCixTools(srv, newServerRegistry())
}

func TestIsCleanShutdown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"context canceled", context.Canceled, true},
		{"io.EOF", io.EOF, true},
		{"wrapped EOF", fmt.Errorf("read: %w", io.EOF), true},
		{"server is closing sentinel", errors.New("server is closing: EOF"), true},
		{"real failure", errors.New("connection refused"), false},
		{"deadline exceeded is not shutdown", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCleanShutdown(tc.err); got != tc.want {
				t.Errorf("isCleanShutdown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRelPath(t *testing.T) {
	cases := []struct {
		root, file, want string
	}{
		{"/proj", "/proj/a/b.go", "a/b.go"},
		{"/proj", "/proj/main.go", "main.go"},
		{"/proj", "/other/x.go", "/other/x.go"}, // outside root → left absolute
	}
	for _, tc := range cases {
		if got := relPath(tc.root, tc.file); got != tc.want {
			t.Errorf("relPath(%q, %q) = %q, want %q", tc.root, tc.file, got, tc.want)
		}
	}
}

func TestFormatSearch(t *testing.T) {
	resp := &client.SearchResponse{
		Total: 1,
		Results: []client.SearchResult{{
			FilePath:  "/proj/internal/auth/mw.go",
			Language:  "go",
			BestScore: 0.72,
			Matches: []client.FileMatch{{
				StartLine: 10, EndLine: 14, Score: 0.72,
				ChunkType: "function", SymbolName: "Validate",
				Content: "func Validate() error {\n\treturn nil\n}\n",
			}},
		}},
	}
	out := formatSearch("/proj", resp)
	for _, want := range []string{
		"Found 1 file(s) in /proj",
		"internal/auth/mw.go", // path rendered relative to root
		"[best 0.72]",
		"function Validate",
		"lines 10-14",
		"func Validate() error",
		"```go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatSearch output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestFormattersEmpty(t *testing.T) {
	if got := formatSearch("/p", &client.SearchResponse{}); !strings.Contains(got, "No results") {
		t.Errorf("empty search = %q", got)
	}
	if got := formatDefinitions("/p", "Foo", &client.DefinitionResponse{}); !strings.Contains(got, "No definitions") {
		t.Errorf("empty definitions = %q", got)
	}
	if got := formatReferences("/p", "Foo", &client.ReferenceResponse{}); !strings.Contains(got, "No references") {
		t.Errorf("empty references = %q", got)
	}
	if got := formatSymbols("/p", "Foo", &client.SymbolSearchResponse{}); !strings.Contains(got, "No symbols") {
		t.Errorf("empty symbols = %q", got)
	}
	if got := formatFiles("/p", "foo", &client.FileSearchResponse{}); !strings.Contains(got, "No files") {
		t.Errorf("empty files = %q", got)
	}
	if got := formatProjects(nil); !strings.Contains(got, "No projects") {
		t.Errorf("empty projects = %q", got)
	}
}

func TestFormatDefinitions(t *testing.T) {
	sig := "func NewRouter(d Deps) http.Handler"
	resp := &client.DefinitionResponse{
		Total: 1,
		Results: []client.DefinitionResult{{
			Name: "NewRouter", Kind: "function", FilePath: "/proj/internal/httpapi/router.go",
			Line: 146, Signature: &sig,
		}},
	}
	out := formatDefinitions("/proj", "NewRouter", resp)
	for _, want := range []string{"NewRouter", "function", "internal/httpapi/router.go:146", "func NewRouter(d Deps)"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatDefinitions missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestFormatServers(t *testing.T) {
	out := formatServers([]serverSummary{
		{Name: "default", URL: "http://localhost:21847", IsDefault: true},
		{Name: "corporate", URL: "https://cix.corp.internal", IsDefault: false},
	})
	for _, want := range []string{"2 cix server(s)", "default", "http://localhost:21847", "(default)", "corporate", "https://cix.corp.internal"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatServers missing %q\n--- got ---\n%s", want, out)
		}
	}
	if got := formatServers(nil); !strings.Contains(got, "No cix servers") {
		t.Errorf("empty servers = %q", got)
	}
}

func TestFormatProjects(t *testing.T) {
	projs := []client.Project{
		{HostPath: "/repo/a", Status: "indexed", Languages: []string{"go"}, Stats: client.ProjectStats{TotalFiles: 12}},
	}
	out := formatProjects(projs)
	for _, want := range []string{"1 indexed project", "/repo/a", "indexed", "12 files", "go"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatProjects missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// TestMcpResolveProject exercises the server-centric project resolution: it
// resolves only against the server's registered projects and never consults the
// working directory.
func TestMcpResolveProject(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			listProjectsHandler([]string{"/repo/a", "/repo/b", "github.com/owner/repo@main"})(w, r)
			return
		}
		apiError(w, http.StatusNotFound, "not found")
	})
	c := client.New(srv.URL, "test-key")

	// Exact registered host_path (local) resolves to itself.
	if got, err := mcpResolveProject(c, "/repo/b"); err != nil || got != "/repo/b" {
		t.Errorf("resolve exact = %q, %v; want /repo/b", got, err)
	}
	// Exact registered host_path (external id) resolves to itself.
	if got, err := mcpResolveProject(c, "github.com/owner/repo@main"); err != nil || got != "github.com/owner/repo@main" {
		t.Errorf("resolve external = %q, %v; want external id", got, err)
	}
	// Absolute path inside a registered local root resolves up to its root.
	if got, err := mcpResolveProject(c, "/repo/a/internal/sub"); err != nil || got != "/repo/a" {
		t.Errorf("resolve subdir = %q, %v; want /repo/a", got, err)
	}
	// Unknown project → error listing the available projects (no cwd fallback,
	// no silent passthrough).
	if _, err := mcpResolveProject(c, "/somewhere/else"); err == nil {
		t.Error("resolve unknown: want error, got nil")
	} else if !strings.Contains(err.Error(), "/repo/a") {
		t.Errorf("unknown-project error should list projects, got: %v", err)
	}
	// Empty → error (no implicit default project).
	if _, err := mcpResolveProject(c, "  "); err == nil {
		t.Error("resolve empty: want error, got nil")
	}
}

// TestMcpResolveWorkspace resolves a workspace by id or name against the server.
func TestMcpResolveWorkspace(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			writeJSON(w, 200, map[string]any{
				"workspaces": []map[string]string{
					{"id": "ws_01", "name": "Backend", "description": "all backend repos"},
				},
				"total": 1,
			})
			return
		}
		apiError(w, http.StatusNotFound, "not found")
	})
	c := client.New(srv.URL, "test-key")

	if got, err := mcpResolveWorkspace(c, "ws_01"); err != nil || got != "ws_01" {
		t.Errorf("resolve by id = %q, %v; want ws_01", got, err)
	}
	if got, err := mcpResolveWorkspace(c, "backend"); err != nil || got != "ws_01" {
		t.Errorf("resolve by name (case-insensitive) = %q, %v; want ws_01", got, err)
	}
	if _, err := mcpResolveWorkspace(c, "nope"); err == nil {
		t.Error("resolve unknown workspace: want error, got nil")
	}
	if _, err := mcpResolveWorkspace(c, ""); err == nil {
		t.Error("resolve empty workspace: want error, got nil")
	}
}

func TestMcpResolveScopePaths(t *testing.T) {
	got := mcpResolveScopePaths("/repo/a", []string{"internal/auth", "/abs/path"})
	want := []string{"/repo/a/internal/auth", "/abs/path"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scope[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if mcpResolveScopePaths("/r", nil) != nil {
		t.Error("nil input should give nil output")
	}
}

func TestFormatWorkspaceSearch(t *testing.T) {
	resp := &client.WorkspaceSearchResponse{
		Status: "ok",
		Projects: []client.WorkspaceSearchProject{
			{ProjectPath: "/repo/a", Label: "service-a", ProjectScore: 0.81, NumHits: 4, BM25Score: 0.42, DenseScore: 0.56},
		},
		Chunks: []client.WorkspaceSearchChunk{
			{ProjectPath: "/repo/a", FilePath: "/repo/a/auth/mw.go", StartLine: 10, EndLine: 12,
				SymbolName: "Validate", Language: "go", Score: 0.77, Content: "func Validate() {}"},
		},
	}
	out := formatWorkspaceSearch(resp)
	for _, want := range []string{
		"1 repo(s) ranked", "service-a", "/repo/a", "score 0.81",
		"bm25 0.42", "dense 0.56",
		"auth/mw.go:10", "go Validate", "func Validate()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatWorkspaceSearch missing %q\n--- got ---\n%s", want, out)
		}
	}
	if got := formatWorkspaceSearch(&client.WorkspaceSearchResponse{}); !strings.Contains(got, "No matches") {
		t.Errorf("empty workspace search = %q", got)
	}
	// An empty result caused by repo failures must still warn about incomplete
	// coverage rather than report a clean "no matches".
	if got := formatWorkspaceSearch(&client.WorkspaceSearchResponse{Status: "partial_failure"}); !strings.Contains(got, "INCOMPLETELY") {
		t.Errorf("empty partial_failure should warn about incomplete coverage, got %q", got)
	}
}
