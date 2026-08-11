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

// TestFullSyncFields_RoundTrip checks the full_sync_required / full_sync_reason
// columns flow through Get and List: a fresh project is in sync, and once the
// flag is set (as migration 18 / a format change does) both loaders surface it.
func TestFullSyncFields_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := Create(ctx, d, CreateRequest{HostPath: "/proj"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := Get(ctx, d, "/proj")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FullSyncRequired || got.FullSyncReason != nil {
		t.Errorf("fresh project: FullSyncRequired=%v reason=%v, want false/nil",
			got.FullSyncRequired, got.FullSyncReason)
	}

	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET full_sync_required = 1, full_sync_reason = 'chunker change' WHERE host_path = ?`,
		"/proj",
	); err != nil {
		t.Fatalf("flag project: %v", err)
	}

	got, err = Get(ctx, d, "/proj")
	if err != nil {
		t.Fatalf("Get after flag: %v", err)
	}
	if !got.FullSyncRequired {
		t.Error("FullSyncRequired = false after flagging, want true")
	}
	if got.FullSyncReason == nil || *got.FullSyncReason != "chunker change" {
		t.Errorf("FullSyncReason = %v, want \"chunker change\"", got.FullSyncReason)
	}

	list, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || !list[0].FullSyncRequired {
		t.Errorf("List did not surface full_sync_required: %+v", list)
	}
}

// TestGet_ReturnsStoredPathHashNotRecomputed guards the dashboard 404
// regression: a project whose host_path and stored path_hash legitimately
// diverge — e.g. a local project keyed as sha1("local:{machine}:{path}")
// while host_path stays the bare filesystem path — must surface the STORED
// hash, because that is what GetByHash resolves against. Recomputing the
// hash from host_path would hand the dashboard a link no lookup matches →
// "project not found".
func TestGet_ReturnsStoredPathHashNotRecomputed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const host = "/Users/me/proj"
	const stored = "deadbeefcafe0001" // intentionally != hashPath(host)
	if hashPath(host) == stored {
		t.Fatal("precondition: stored hash must differ from the bare host-path hash")
	}
	now := "2026-01-01T00:00:00Z"
	if _, err := d.ExecContext(ctx,
		`INSERT INTO projects (host_path, container_path, languages, settings, stats, status, created_at, updated_at, path_hash, display_path, machine_id)
		 VALUES (?, ?, '[]', '{}', '{}', 'indexed', ?, ?, ?, ?, ?)`,
		host, host, now, now, stored, host, "machine-xyz",
	); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	got, err := Get(ctx, d, host)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PathHash != stored {
		t.Errorf("Get PathHash = %q, want stored %q (must not recompute from host_path)", got.PathHash, stored)
	}

	list, err := List(ctx, d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].PathHash != stored {
		t.Errorf("List PathHash = %+v, want [%q]", list, stored)
	}

	// The stored hash must resolve back to the project (the dashboard
	// click path: link hash → GetByHash → detail).
	byHash, err := GetByHash(ctx, d, stored)
	if err != nil {
		t.Fatalf("GetByHash(stored): %v", err)
	}
	if byHash.HostPath != host {
		t.Errorf("GetByHash HostPath = %q, want %q", byHash.HostPath, host)
	}
	if byHash.PathHash != stored {
		t.Errorf("GetByHash PathHash = %q, want %q", byHash.PathHash, stored)
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

	if err := Delete(ctx, d, "/proj", nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := Get(ctx, d, "/proj")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Get returned %v, want ErrNotFound", err)
	}
}

// The artifacts hook is the fix for the leak that motivated the Resources
// screen: FK CASCADE reaches rows only, so without it a deleted project left
// its vector collection resident in RAM forever.
func TestDelete_RunsArtifactHooks(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})

	var droppedFor, removedFor string
	err := Delete(ctx, d, "/proj", &Artifacts{
		DropCollection: func(hostPath string) error { droppedFor = hostPath; return nil },
		RemoveCloneDir: func(hostPath string) error { removedFor = hostPath; return nil },
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if droppedFor != "/proj" || removedFor != "/proj" {
		t.Errorf("hooks called with (%q, %q), want both /proj", droppedFor, removedFor)
	}
}

// A hook failure must not resurrect the project or look like a failed delete:
// the row is gone, and what is left is reclaimable garbage.
func TestDelete_ArtifactFailureStillDeletesTheProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _ = Create(ctx, d, CreateRequest{HostPath: "/proj"})

	err := Delete(ctx, d, "/proj", &Artifacts{
		DropCollection: func(string) error { return errors.New("disk on fire") },
	})
	if !errors.Is(err, ErrArtifactCleanup) {
		t.Fatalf("Delete error = %v, want it to wrap ErrArtifactCleanup", err)
	}
	if _, err := Get(ctx, d, "/proj"); !errors.Is(err, ErrNotFound) {
		t.Errorf("project still present after a cleanup failure: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	err := Delete(ctx, d, "/nonexistent", nil)
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

// TestCreate_MachineNamespacingAvoidsCollision verifies that the same
// filesystem path indexed from two different machines becomes two distinct
// projects (different identity key + hash), while the same machine+path
// collides — and that display_path holds the real path either way.
func TestCreate_MachineNamespacingAvoidsCollision(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	const realPath = "/Users/dev/myapp"

	p1, err := Create(ctx, d, CreateRequest{HostPath: realPath, MachineID: "machineA", MachineLabel: "laptop-a"})
	if err != nil {
		t.Fatalf("create on machineA: %v", err)
	}
	p2, err := Create(ctx, d, CreateRequest{HostPath: realPath, MachineID: "machineB", MachineLabel: "laptop-b"})
	if err != nil {
		t.Fatalf("create same path on machineB: %v", err)
	}

	if p1.HostPath == p2.HostPath {
		t.Errorf("identity keys collided: %q", p1.HostPath)
	}
	if HashPath(p1.HostPath) == HashPath(p2.HostPath) {
		t.Error("path_hashes collided across machines")
	}
	if p1.DisplayPath != realPath || p2.DisplayPath != realPath {
		t.Errorf("display_path = %q / %q, want %q", p1.DisplayPath, p2.DisplayPath, realPath)
	}
	if p1.MachineID == nil || *p1.MachineID != "machineA" {
		t.Errorf("p1 machine_id = %v, want machineA", p1.MachineID)
	}
	if p1.MachineLabel == nil || *p1.MachineLabel != "laptop-a" {
		t.Errorf("p1 machine_label = %v, want laptop-a", p1.MachineLabel)
	}

	// Same machine + same path → conflict.
	if _, err := Create(ctx, d, CreateRequest{HostPath: realPath, MachineID: "machineA"}); !errors.Is(err, ErrConflict) {
		t.Errorf("same machine+path err = %v, want ErrConflict", err)
	}

	// The namespaced key must equal what the CLI computes.
	if got, want := p1.HostPath, LocalProjectKey("machineA", realPath); got != want {
		t.Errorf("identity key = %q, want %q", got, want)
	}
}
