// Package eval is the gate that decides whether the refs-heuristic call
// graph is good enough to feed Louvain. Hand-labeled (caller, callee)
// pairs across Go / Python / TypeScript fixtures pass through the full
// chunker → symbols/refs → callgraph.Build pipeline, and the harness
// asserts the labeled edge appears in call_edges.
//
// Threshold: precision per language must be >= 0.6, matching the Plan
// agent's mitigation rule. If a language falls below, we don't ship the
// downstream Louvain feature with this graph as the substrate — the
// reasonable fallback is the symbol co-occurrence graph (edges between
// symbols in the same file), implemented as a separate Source in
// callgraph.
package eval

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dvcdsys/code-index/server/internal/callgraph"
	"github.com/dvcdsys/code-index/server/internal/chunker"
	"github.com/dvcdsys/code-index/server/internal/db"
)

// labeledPair is one (caller, callee) expectation. The caller and callee
// values are symbol NAMES — the harness resolves them via the symbols
// table after chunking. Names that match multiple symbols (overloads)
// are disallowed in fixtures; pick unique names.
type labeledPair struct {
	Caller string
	Callee string
}

// langFixture pairs a small piece of source code with the (caller, callee)
// edges the heuristic should produce. The fixture is exercised end-to-end:
// chunked → symbols+refs persisted → callgraph.Build → edge presence asserted.
type langFixture struct {
	Name      string // displayed in test output
	Language  string // language constant chunker recognises
	FilePath  string // virtual path; only the extension matters for some chunkers
	Source    string
	Expected  []labeledPair
}

// Threshold below which we'd fall back to a co-occurrence graph. 0.6 is
// the Plan agent's recommended floor; tighten once we have real data.
const precisionFloor = 0.6

// fixtures kept tiny on purpose — every line is curated. A typical real
// project has thousands of refs; the eval is about checking the heuristic
// resolves the SHAPE correctly on small but realistic code, not about
// stress-testing throughput.
var fixtures = []langFixture{
	{
		Name:     "go-handlers",
		Language: "go",
		FilePath: "handlers.go",
		Source: `package main

import "fmt"

type Server struct{}

func (s *Server) HandleLogin(req string) string {
	user := parseUser(req)
	if !s.validate(user) {
		return "denied"
	}
	return s.issueToken(user)
}

func (s *Server) validate(user string) bool {
	return user != ""
}

func (s *Server) issueToken(user string) string {
	return fmt.Sprintf("token:%s", user)
}

func parseUser(req string) string {
	return req
}

func main() {
	s := &Server{}
	s.HandleLogin("user1")
}
`,
		Expected: []labeledPair{
			{Caller: "HandleLogin", Callee: "parseUser"},
			{Caller: "HandleLogin", Callee: "validate"},
			{Caller: "HandleLogin", Callee: "issueToken"},
			{Caller: "main", Callee: "HandleLogin"},
		},
	},
	{
		Name:     "python-pipeline",
		Language: "python",
		FilePath: "pipeline.py",
		Source: `class Pipeline:
    def run(self, source):
        rows = self.fetch(source)
        cleaned = self.clean(rows)
        return self.persist(cleaned)

    def fetch(self, source):
        return open_source(source)

    def clean(self, rows):
        return [r for r in rows if r]

    def persist(self, rows):
        write_rows(rows)
        return len(rows)


def open_source(name):
    return [name]


def write_rows(rows):
    print(rows)


def main():
    p = Pipeline()
    p.run("local")
`,
		Expected: []labeledPair{
			{Caller: "run", Callee: "fetch"},
			{Caller: "run", Callee: "clean"},
			{Caller: "run", Callee: "persist"},
			{Caller: "fetch", Callee: "open_source"},
			{Caller: "persist", Callee: "write_rows"},
			{Caller: "main", Callee: "run"},
		},
	},
	{
		Name:     "typescript-store",
		Language: "typescript",
		FilePath: "store.ts",
		Source: `function loadConfig(): string {
  return readFile("config.json");
}

function readFile(path: string): string {
  return path;
}

function applyMigrations(cfg: string): void {
  const items = parseItems(cfg);
  runItems(items);
}

function parseItems(cfg: string): string[] {
  return [cfg];
}

function runItems(items: string[]): void {
  console.log(items.length);
}

function bootstrap(): void {
  const cfg = loadConfig();
  applyMigrations(cfg);
}
`,
		Expected: []labeledPair{
			{Caller: "loadConfig", Callee: "readFile"},
			{Caller: "applyMigrations", Callee: "parseItems"},
			{Caller: "applyMigrations", Callee: "runItems"},
			{Caller: "bootstrap", Callee: "loadConfig"},
			{Caller: "bootstrap", Callee: "applyMigrations"},
		},
	},
}

// TestEval runs every fixture, records precision per language, and fails
// the build only when at least one language falls below precisionFloor.
//
// Output format is deliberately verbose — when we eventually swap the
// heuristic, this log gives a clean before/after comparison.
func TestEval(t *testing.T) {
	results := map[string]float64{}
	for _, fx := range fixtures {
		got, total, err := runFixture(t, fx)
		if err != nil {
			t.Fatalf("[%s] fixture failed: %v", fx.Name, err)
		}
		prec := float64(got) / float64(total)
		results[fx.Name] = prec
		t.Logf("[%s] %d / %d expected edges present (precision %.2f)",
			fx.Name, got, total, prec)
	}

	// Sort for stable output regardless of map iteration order.
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	failing := []string{}
	for _, k := range keys {
		if results[k] < precisionFloor {
			failing = append(failing, fmt.Sprintf("%s=%.2f", k, results[k]))
		}
	}
	if len(failing) > 0 {
		t.Fatalf("call-graph eval failed precision floor %.2f for: %v\nFall back to co-occurrence graph (callgraph.SourceCoOccurrence) before shipping PR5.",
			precisionFloor, failing)
	}
}

// runFixture executes the full chunk → persist → build → assert pipeline
// for one source file. Returns (edges-found, total-expected, err).
func runFixture(t *testing.T, fx langFixture) (int, int, error) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		return 0, 0, fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	const projectPath = "github.com/test/repo@main"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'created', ?, ?, 'abc')`,
		projectPath, projectPath, now, now); err != nil {
		return 0, 0, fmt.Errorf("seed project: %w", err)
	}

	// Activate every language the chunker registry knows about — defaults
	// to all when nothing is configured by tests (chunker uses package
	// state). Configure with an empty slice means "default registry".
	chunker.Configure(nil)

	chunks, refs, err := chunker.ChunkFile(fx.FilePath, fx.Source, fx.Language, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("chunk: %w", err)
	}

	if err := persistChunks(database, projectPath, fx.Language, chunks, refs); err != nil {
		return 0, 0, err
	}

	if _, err := callgraph.Build(context.Background(), database, projectPath, callgraph.DefaultOptions()); err != nil {
		return 0, 0, fmt.Errorf("build: %w", err)
	}

	got := 0
	for _, pair := range fx.Expected {
		ok, err := hasEdgeByName(database, projectPath, pair.Caller, pair.Callee)
		if err != nil {
			return 0, 0, err
		}
		if ok {
			got++
		} else {
			t.Logf("[%s] MISSING edge: %s → %s", fx.Name, pair.Caller, pair.Callee)
		}
	}
	return got, len(fx.Expected), nil
}

// persistChunks mirrors indexer.ProcessFiles' symbol+ref persistence
// without the embedding loop. Lifted to keep the eval harness independent
// of the embeddings sidecar.
func persistChunks(d *sql.DB, projectPath, language string, chunks []chunker.Chunk, refs []chunker.Reference) error {
	for _, c := range chunks {
		if c.SymbolName == nil {
			continue
		}
		switch c.ChunkType {
		case "function", "class", "method", "type":
		default:
			continue
		}
		var sig string
		if c.SymbolSignature != nil {
			sig = *c.SymbolSignature
		}
		var parent any
		if c.ParentName != nil && *c.ParentName != "" {
			parent = *c.ParentName
		} else {
			parent = nil
		}
		if _, err := d.Exec(
			`INSERT INTO symbols (id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), projectPath, *c.SymbolName, c.ChunkType, c.FilePath,
			c.StartLine, c.EndLine, language, sig, parent,
		); err != nil {
			return fmt.Errorf("insert symbol: %w", err)
		}
	}
	for _, r := range refs {
		if _, err := d.Exec(
			`INSERT INTO refs (project_path, name, file_path, line, col, language)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			projectPath, r.Name, r.FilePath, r.Line, r.Col, language,
		); err != nil {
			return fmt.Errorf("insert ref: %w", err)
		}
	}
	return nil
}

// hasEdgeByName resolves both names to symbol ids and checks call_edges.
// Returns false (not an error) when either name resolves to zero
// symbols — that's typically a missing chunk and is informative for the
// harness output.
func hasEdgeByName(d *sql.DB, projectPath, caller, callee string) (bool, error) {
	var n int
	err := d.QueryRow(`
		SELECT COUNT(*) FROM call_edges
		 WHERE project_path = ?
		   AND caller_symbol IN (SELECT id FROM symbols WHERE project_path = ? AND name = ?)
		   AND callee_symbol IN (SELECT id FROM symbols WHERE project_path = ? AND name = ?)`,
		projectPath, projectPath, caller, projectPath, callee).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
