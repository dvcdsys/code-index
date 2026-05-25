package callgraph

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/db"
)

// fixtureDB stands up a project with hand-crafted symbols + refs so we
// can assert the heuristic's outputs without driving the full indexer.
// Each test calls this helper and supplies a seed function.
func fixtureDB(t *testing.T, seed func(d *sql.DB)) (*sql.DB, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	const projectPath = "github.com/test/repo@main"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.Exec(`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash)
		VALUES (?, ?, '[]', '{}', '{}', 'created', ?, ?, 'abc')`,
		projectPath, projectPath, now, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	seed(d)
	return d, projectPath
}

func insertSymbol(t *testing.T, d *sql.DB, projectPath, id, name, kind, file string, line, end int, parent string) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO symbols (id, project_path, name, kind, file_path, line, end_line, language, signature, parent_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'go', '', NULLIF(?, ''))`,
		id, projectPath, name, kind, file, line, end, parent,
	); err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
}

func insertRef(t *testing.T, d *sql.DB, projectPath, name, file string, line int) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO refs (project_path, name, file_path, line, col, language)
		 VALUES (?, ?, ?, ?, 0, 'go')`,
		projectPath, name, file, line,
	); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
}

func loadEdges(t *testing.T, d *sql.DB, projectPath string) []edgeRow {
	t.Helper()
	rows, err := d.Query(`SELECT caller_symbol, callee_symbol, weight FROM call_edges WHERE project_path = ?`, projectPath)
	if err != nil {
		t.Fatalf("query edges: %v", err)
	}
	defer rows.Close()
	var out []edgeRow
	for rows.Next() {
		var e edgeRow
		_ = rows.Scan(&e.Caller, &e.Callee, &e.Weight)
		out = append(out, e)
	}
	return out
}

type edgeRow struct {
	Caller, Callee string
	Weight         float64
}

// Happy path: one caller, one callee, in-the-same-file, popcount=1.
// Resulting weight: 1/1 × same-file bonus = 2.0.
func TestBuildSingleEdge(t *testing.T) {
	const pp = "github.com/test/repo@main"
	d, _ := fixtureDB(t, func(d *sql.DB) {
		insertSymbol(t, d, pp, "fn1", "doX", "function", "main.go", 10, 20, "")
		insertSymbol(t, d, pp, "fn2", "helper", "function", "main.go", 30, 35, "")
		insertRef(t, d, pp, "helper", "main.go", 15) // call from inside fn1
	})

	stats, err := Build(context.Background(), d, pp, DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 1 {
		t.Fatalf("expected 1 edge, got %d (stats: %+v)", stats.EdgesAccumulated, stats)
	}
	edges := loadEdges(t, d, pp)
	if len(edges) != 1 {
		t.Fatalf("expected 1 row, got %d", len(edges))
	}
	if edges[0].Caller != "fn1" || edges[0].Callee != "fn2" {
		t.Fatalf("wrong edge direction: %+v", edges[0])
	}
	if edges[0].Weight < 1.9 || edges[0].Weight > 2.1 {
		t.Fatalf("expected weight ~2.0 (same-file bonus), got %v", edges[0].Weight)
	}
}

// popcount=N → weight = 1/N. Refs to a common name (e.g. "init") in
// 25 places: every edge dropped (PopcountDrop=20 default).
func TestPopcountDrop(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "caller", "doX", "function", "caller.go", 10, 20, "")
		// 25 callee definitions of "init" across many files.
		for i := 0; i < 25; i++ {
			file := "a.go"
			if i%2 == 0 {
				file = "b.go"
			}
			id := "init_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			insertSymbol(t, d, pp, id, "init", "function", file, 100+i*10, 105+i*10, "")
		}
		insertRef(t, d, pp, "init", "caller.go", 15)
	})
	pp := "github.com/test/repo@main"
	stats, err := Build(context.Background(), d, pp, DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 0 {
		t.Fatalf("expected 0 edges (popcount drop), got %d", stats.EdgesAccumulated)
	}
}

// Module-scope ref: line lies outside any function's span → no edge.
func TestModuleScopeRefSkipped(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "fn", "main", "function", "main.go", 10, 20, "")
		// Ref at line 5 — before any function in this file.
		insertRef(t, d, pp, "main", "main.go", 5)
	})
	stats, err := Build(context.Background(), d, "github.com/test/repo@main", DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 0 {
		t.Fatalf("expected 0 edges, got %d (stats: %+v)", stats.EdgesAccumulated, stats)
	}
}

// Self-edges (recursion) are dropped — they don't help Louvain.
func TestSelfEdgeDropped(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "fac", "factorial", "function", "f.go", 10, 30, "")
		// Ref to "factorial" from INSIDE factorial — recursion.
		insertRef(t, d, pp, "factorial", "f.go", 15)
	})
	stats, err := Build(context.Background(), d, "github.com/test/repo@main", DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 0 {
		t.Fatalf("expected 0 edges (self-recursion), got %d", stats.EdgesAccumulated)
	}
}

// Cross-file caller/callee → 1/N weight without the same-file bonus.
func TestCrossFileWeight(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "main", "main", "function", "cmd/main.go", 10, 20, "")
		insertSymbol(t, d, pp, "lib1", "helper", "function", "lib/a.go", 10, 15, "")
		insertSymbol(t, d, pp, "lib2", "helper", "function", "lib/b.go", 10, 15, "")
		// popcount=2 → base weight 0.5
		insertRef(t, d, pp, "helper", "cmd/main.go", 15)
	})
	stats, err := Build(context.Background(), d, "github.com/test/repo@main", DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 2 {
		t.Fatalf("expected 2 edges (one per candidate), got %d", stats.EdgesAccumulated)
	}
	edges := loadEdges(t, d, "github.com/test/repo@main")
	for _, e := range edges {
		// Cross-file callees → no same-file bonus → weight = 0.5
		if e.Weight < 0.49 || e.Weight > 0.51 {
			t.Fatalf("expected cross-file weight ≈0.5, got %v", e.Weight)
		}
	}
}

// Same parent (method calls within a class) → 1.5× bonus.
func TestSameParentBonus(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		// Two methods on the same struct, same file.
		insertSymbol(t, d, pp, "m1", "Process", "method", "svc.go", 10, 20, "Service")
		insertSymbol(t, d, pp, "m2", "helper", "method", "svc.go", 25, 35, "Service")
		insertRef(t, d, pp, "helper", "svc.go", 15)
	})
	stats, err := Build(context.Background(), d, "github.com/test/repo@main", DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.EdgesAccumulated != 1 {
		t.Fatalf("expected 1 edge, got %d", stats.EdgesAccumulated)
	}
	edges := loadEdges(t, d, "github.com/test/repo@main")
	// 1/1 × 2.0 (same-file) × 1.5 (same-parent) = 3.0
	if edges[0].Weight < 2.95 || edges[0].Weight > 3.05 {
		t.Fatalf("expected weight ≈3.0 (file+parent bonus), got %v", edges[0].Weight)
	}
}

// Multiple refs to the same callee accumulate into one row (sum of weights).
func TestAccumulation(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "fn", "main", "function", "main.go", 1, 100, "")
		insertSymbol(t, d, pp, "helper", "helper", "function", "main.go", 200, 210, "")
		insertRef(t, d, pp, "helper", "main.go", 5)
		insertRef(t, d, pp, "helper", "main.go", 6)
		insertRef(t, d, pp, "helper", "main.go", 7)
	})
	stats, err := Build(context.Background(), d, "github.com/test/repo@main", DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := loadEdges(t, d, "github.com/test/repo@main")
	if len(edges) != 1 {
		t.Fatalf("expected 1 distinct edge, got %d", len(edges))
	}
	// 3 refs × (1/1 × 2.0 same-file) = 6.0
	if edges[0].Weight < 5.9 || edges[0].Weight > 6.1 {
		t.Fatalf("expected accumulated weight ≈6.0, got %v", edges[0].Weight)
	}
	if stats.EdgesEmitted != 3 || stats.EdgesAccumulated != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// Idempotency: running Build twice yields the same edge set, not a duplicate.
func TestIdempotent(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "a", "doX", "function", "main.go", 10, 20, "")
		insertSymbol(t, d, pp, "b", "helper", "function", "main.go", 30, 35, "")
		insertRef(t, d, pp, "helper", "main.go", 15)
	})
	pp := "github.com/test/repo@main"
	if _, err := Build(context.Background(), d, pp, DefaultOptions()); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	first := loadEdges(t, d, pp)
	if _, err := Build(context.Background(), d, pp, DefaultOptions()); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	second := loadEdges(t, d, pp)
	if len(first) != len(second) {
		t.Fatalf("idempotency broken: %d vs %d edges", len(first), len(second))
	}
}

func TestCountEdges(t *testing.T) {
	d, _ := fixtureDB(t, func(d *sql.DB) {
		pp := "github.com/test/repo@main"
		insertSymbol(t, d, pp, "a", "doX", "function", "main.go", 10, 20, "")
		insertSymbol(t, d, pp, "b", "helper", "function", "main.go", 30, 35, "")
		insertRef(t, d, pp, "helper", "main.go", 15)
	})
	pp := "github.com/test/repo@main"
	if _, err := Build(context.Background(), d, pp, DefaultOptions()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	n, err := CountEdges(context.Background(), d, pp)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
}
