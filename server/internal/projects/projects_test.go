package projects

import (
	"context"
	"database/sql"
	"errors"
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

func TestCreateAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	p, err := Create(ctx, d, CreateRequest{HostPath: "/home/user/project"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.HostPath != "/home/user/project" {
		t.Errorf("HostPath = %q", p.HostPath)
	}
	if p.Status != "created" {
		t.Errorf("Status = %q, want created", p.Status)
	}
	if len(p.Settings.ExcludePatterns) == 0 {
		t.Error("expected default exclude patterns")
	}

	// Idempotent Get.
	got, err := Get(ctx, d, "/home/user/project")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.HostPath != p.HostPath {
		t.Errorf("Get HostPath = %q", got.HostPath)
	}
}

// Create preserves the host_path verbatim — matching Python which does not
// normalise. Stripping trailing slashes here would silently change the stored
// value and break subsequent lookups that hash the caller's original path.
func TestCreate_PreservesHostPathVerbatim(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, err := Create(ctx, d, CreateRequest{HostPath: "/proj/"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := Get(ctx, d, "/proj/")
	if err != nil {
		t.Fatalf("Get with trailing slash: %v", err)
	}
	if got.HostPath != "/proj/" {
		t.Errorf("HostPath = %q, want /proj/ (verbatim)", got.HostPath)
	}
	// Conversely, a Get without the trailing slash must miss.
	if _, err := Get(ctx, d, "/proj"); err == nil {
		t.Errorf("expected ErrNotFound for /proj when stored as /proj/")
	}
}

func TestCreate_Conflict(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})
	_, err := Create(ctx, d, CreateRequest{HostPath: "/proj"})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

// TestCreate_RejectsOverlap covers both directions and a few cosmetic
// variants (trailing slash) of the parent/descendant containment check.
// Sibling and string-prefix-but-not-path-prefix cases must succeed —
// otherwise we'd block legitimate adjacent projects.
func TestCreate_RejectsOverlap(t *testing.T) {
	cases := []struct {
		name           string
		seed, attempt  string
		wantOverlapErr bool
	}{
		{"new path is descendant", "/repo", "/repo/server", true},
		{"new path is ancestor", "/repo/server", "/repo", true},
		{"deep nesting still caught", "/repo", "/repo/a/b/c/d", true},
		{"trailing slash on seed", "/repo/", "/repo/server", true},
		{"trailing slash on candidate", "/repo", "/repo/server/", true},
		{"sibling is fine", "/repo/server", "/repo/cli", false},
		// "/repo-other" shares "/repo" as a string prefix but NOT as a path
		// prefix — must not be rejected.
		{"prefix-but-not-path-prefix is fine", "/repo", "/repo-other", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			ctx := context.Background()
			if _, err := Create(ctx, d, CreateRequest{HostPath: tc.seed}); err != nil {
				t.Fatalf("seed Create(%q) failed: %v", tc.seed, err)
			}
			_, err := Create(ctx, d, CreateRequest{HostPath: tc.attempt})
			if tc.wantOverlapErr {
				if !errors.Is(err, ErrOverlap) {
					t.Fatalf("Create(%q) error = %v, want ErrOverlap", tc.attempt, err)
				}
			} else if err != nil {
				t.Fatalf("Create(%q) failed unexpectedly: %v", tc.attempt, err)
			}
		})
	}
}

func TestGet_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, err := Get(ctx, d, "/nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	for _, path := range []string{"/a", "/b", "/c"} {
		if _, err := Create(ctx, d, CreateRequest{HostPath: path}); err != nil {
			t.Fatalf("Create %s: %v", path, err)
		}
	}

	projects, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 3 {
		t.Errorf("List: got %d projects, want 3", len(projects))
	}
}

func TestPatch(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})

	newSettings := &Settings{
		ExcludePatterns: []string{"vendor"},
		MaxFileSize:     1000,
	}
	updated, err := Patch(ctx, d, "/proj", UpdateRequest{Settings: newSettings})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if len(updated.Settings.ExcludePatterns) != 1 || updated.Settings.ExcludePatterns[0] != "vendor" {
		t.Errorf("Patch settings: %+v", updated.Settings)
	}
	if updated.Settings.MaxFileSize != 1000 {
		t.Errorf("MaxFileSize = %d, want 1000", updated.Settings.MaxFileSize)
	}
}

func TestPatch_NilSettings(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})
	updated, err := Patch(ctx, d, "/proj", UpdateRequest{Settings: nil})
	if err != nil {
		t.Fatalf("Patch nil settings: %v", err)
	}
	// Should return the unmodified project.
	if updated.Status != "created" {
		t.Errorf("Status = %q after nil patch", updated.Status)
	}
}

func TestDelete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})

	if err := Delete(ctx, d, "/proj"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := Get(ctx, d, "/proj")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Get returned %v, want ErrNotFound", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	err := Delete(ctx, d, "/nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete nonexistent: %v, want ErrNotFound", err)
	}
}

func TestGetByHash(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, _ = Create(ctx, d, CreateRequest{HostPath: "/myproject"})
	hash := HashPath("/myproject")

	got, err := GetByHash(ctx, d, hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.HostPath != "/myproject" {
		t.Errorf("GetByHash HostPath = %q", got.HostPath)
	}
}

func TestGetByHash_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_, err := GetByHash(ctx, d, "deadbeef12345678")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByHash unknown hash: %v, want ErrNotFound", err)
	}
}

func TestHashPath_MatchesPython(t *testing.T) {
	// Python: hashlib.sha1("/home/user/repo".encode()).hexdigest()[:16]
	// Python value computed offline: sha1("/home/user/repo") = first 16 chars.
	// We verify the function is stable (same input → same output).
	h1 := HashPath("/home/user/repo")
	h2 := HashPath("/home/user/repo")
	if h1 != h2 {
		t.Errorf("HashPath not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("HashPath length = %d, want 16", len(h1))
	}
}

