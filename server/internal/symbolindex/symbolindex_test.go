package symbolindex

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func ptr(s string) *string { return &s }

func TestUpsertAndSearchByName(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Insert a project row first (foreign key).
	_, err := d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	symbols := []Symbol{
		{Name: "MyFunc", Kind: "function", FilePath: "/proj/main.go", Line: 5, EndLine: 10, Language: "go", Signature: ptr("func MyFunc()")},
		{Name: "MyClass", Kind: "class", FilePath: "/proj/main.go", Line: 15, EndLine: 30, Language: "go"},
	}
	if err := UpsertSymbols(ctx, d, "/proj", symbols); err != nil {
		t.Fatalf("UpsertSymbols: %v", err)
	}

	// Exact match.
	got, err := SearchByName(ctx, d, "/proj", "MyFunc", nil, 10)
	if err != nil {
		t.Fatalf("SearchByName: %v", err)
	}
	if len(got) != 1 || got[0].Name != "MyFunc" {
		t.Errorf("SearchByName exact: got %v", got)
	}

	// Prefix match.
	got, err = SearchByName(ctx, d, "/proj", "My", nil, 10)
	if err != nil {
		t.Fatalf("SearchByName prefix: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("SearchByName prefix: want 2 results, got %d", len(got))
	}

	// Kind filter.
	got, err = SearchByName(ctx, d, "/proj", "My", []string{"class"}, 10)
	if err != nil {
		t.Fatalf("SearchByName kind filter: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "class" {
		t.Errorf("SearchByName kind filter: got %v", got)
	}
}

func TestDeleteByFile(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

	symbols := []Symbol{
		{Name: "F1", Kind: "function", FilePath: "/proj/a.go", Line: 1, EndLine: 5, Language: "go"},
		{Name: "F2", Kind: "function", FilePath: "/proj/b.go", Line: 1, EndLine: 5, Language: "go"},
	}
	if err := UpsertSymbols(ctx, d, "/proj", symbols); err != nil {
		t.Fatalf("UpsertSymbols: %v", err)
	}

	if err := DeleteByFile(ctx, d, "/proj", "/proj/a.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}

	got, _ := GetProjectSymbols(ctx, d, "/proj")
	if len(got) != 1 || got[0].FilePath != "/proj/b.go" {
		t.Errorf("after DeleteByFile: %v", got)
	}
}

func TestSearchDefinitions(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

	symbols := []Symbol{
		{Name: "Handler", Kind: "function", FilePath: "/proj/main.go", Line: 1, EndLine: 5, Language: "go"},
	}
	if err := UpsertSymbols(ctx, d, "/proj", symbols); err != nil {
		t.Fatalf("UpsertSymbols: %v", err)
	}

	got, err := SearchDefinitions(ctx, d, "/proj", "Handler", "", "", 10)
	if err != nil {
		t.Fatalf("SearchDefinitions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	if got[0].Name != "Handler" {
		t.Errorf("Name = %q, want Handler", got[0].Name)
	}
}

func TestUpsertAndSearchReferences(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

	refs := []Reference{
		{Name: "MyFunc", FilePath: "/proj/a.go", Line: 10, Col: 5, Language: "go"},
		{Name: "MyFunc", FilePath: "/proj/b.go", Line: 20, Col: 0, Language: "go"},
		{Name: "Other", FilePath: "/proj/a.go", Line: 11, Col: 0, Language: "go"},
	}
	if err := UpsertReferences(ctx, d, "/proj", refs); err != nil {
		t.Fatalf("UpsertReferences: %v", err)
	}

	got, err := SearchReferences(ctx, d, "/proj", "MyFunc", "", 50)
	if err != nil {
		t.Fatalf("SearchReferences: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 refs, got %d", len(got))
	}

	// Filter by file.
	got, err = SearchReferences(ctx, d, "/proj", "MyFunc", "/proj/a.go", 50)
	if err != nil {
		t.Fatalf("SearchReferences file filter: %v", err)
	}
	if len(got) != 1 || got[0].FilePath != "/proj/a.go" {
		t.Errorf("SearchReferences file filter: %v", got)
	}
}

func TestDeleteRefsByFile(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

	refs := []Reference{
		{Name: "F", FilePath: "/proj/a.go", Line: 1, Col: 0, Language: "go"},
		{Name: "F", FilePath: "/proj/b.go", Line: 1, Col: 0, Language: "go"},
	}
	_ = UpsertReferences(ctx, d, "/proj", refs)

	if err := DeleteRefsByFile(ctx, d, "/proj", "/proj/a.go"); err != nil {
		t.Fatalf("DeleteRefsByFile: %v", err)
	}

	got, _ := SearchReferences(ctx, d, "/proj", "F", "", 50)
	if len(got) != 1 || got[0].FilePath != "/proj/b.go" {
		t.Errorf("after DeleteRefsByFile: %v", got)
	}
}

func TestCountProjectSymbols(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`, "/proj", "/proj", "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

	n, err := CountProjectSymbols(ctx, d, "/proj")
	if err != nil {
		t.Fatalf("CountProjectSymbols: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 initially, got %d", n)
	}

	_ = UpsertSymbols(ctx, d, "/proj", []Symbol{
		{Name: "A", Kind: "function", FilePath: "/proj/f.go", Line: 1, EndLine: 2, Language: "go"},
		{Name: "B", Kind: "function", FilePath: "/proj/f.go", Line: 3, EndLine: 4, Language: "go"},
	})

	n, _ = CountProjectSymbols(ctx, d, "/proj")
	if n != 2 {
		t.Errorf("want 2, got %d", n)
	}
}
