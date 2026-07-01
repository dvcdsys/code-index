package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	apidb "github.com/dvcdsys/code-index/server/internal/db"
	"github.com/dvcdsys/code-index/server/internal/groups"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/projects"
	"github.com/dvcdsys/code-index/server/internal/repocloner"
	"github.com/dvcdsys/code-index/server/internal/repolocks"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
	"github.com/dvcdsys/code-index/server/internal/workspaces"
)

// newFileFixture mirrors newAuthFixture but wires DataDir (a temp dir) and a
// RepoLocks registry — both required by the file/tree handlers. The lock
// registry is reachable via f.Deps.RepoLocks so the concurrency test can drive
// the writer side.
func newFileFixture(t *testing.T) *authTestFixture {
	t.Helper()
	database, err := apidb.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	usrSvc := users.New(database)
	sessSvc := sessions.New(database)
	akSvc := apikeys.New(database)

	u, err := usrSvc.Create(context.Background(), "admin@example.com", "secret-password", users.RoleAdmin, false)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	full, _, err := akSvc.Generate(context.Background(), u.ID, "test-key")
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	deps := Deps{
		DB:             database,
		ServerVersion:  "0.0.0-test",
		APIVersion:     "v1",
		EmbeddingModel: "test-model",
		Users:          usrSvc,
		Sessions:       sessSvc,
		APIKeys:        akSvc,
		Groups:         groups.New(database),
		Workspaces:     workspaces.New(database),
		DataDir:        t.TempDir(),
		RepoLocks:      repolocks.New(),
	}
	return &authTestFixture{Router: NewRouter(deps), Deps: deps, UserID: u.ID, FullKey: full}
}

// seedExternalProject inserts a project row + git_repos peer (the ownerless,
// admin-administered external project shape). github_url/branch are derived
// from hostPath ("github.com/owner/repo@branch") so each seeded repo is unique
// against the git_repos UNIQUE(github_url, branch) constraint.
func seedExternalProject(t *testing.T, f *authTestFixture, hostPath string) {
	t.Helper()
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: hostPath}); err != nil {
		t.Fatalf("create external project: %v", err)
	}
	repo, branch := hostPath, "main"
	if i := strings.LastIndex(hostPath, "@"); i >= 0 {
		repo, branch = hostPath[:i], hostPath[i+1:]
	}
	if _, err := f.Deps.DB.ExecContext(t.Context(),
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES (?, ?, ?, 's', '2024-01-01', '2024-01-01')`,
		hostPath, "https://"+repo, branch); err != nil {
		t.Fatalf("insert git_repos: %v", err)
	}
}

// writeCheckout materialises files on disk under the project's checkout dir
// (<DataDir>/repos/<path_hash>/) and returns that dir.
func writeCheckout(t *testing.T, f *authTestFixture, hostPath string, files map[string]string) string {
	t.Helper()
	dir := repocloner.LocalDirFor(f.Deps.DataDir, projects.HashPath(hostPath))
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func adminLogin(t *testing.T, f *authTestFixture) string {
	t.Helper()
	return sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
}

func TestReadProjectFile_AccessGating(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	bobCookie := seedUser(t, f, adminCookie, "bob@example.com", "bobpass1234")

	// 1. Local project (owner set, no git_repos) → 409 local-unsupported.
	localPath := "/tmp/cix-local-proj"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: localPath, OwnerUserID: f.UserID}); err != nil {
		t.Fatalf("create local: %v", err)
	}
	localHash := projects.HashPath(localPath)
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+localHash+"/file", map[string]any{"file": "README.md"}); rr.Code != http.StatusConflict {
		t.Errorf("local file = %d, want 409; body=%s", rr.Code, b)
	}

	// 2. External project, no checkout on disk → 409 not-yet-available.
	noCheckout := "github.com/x/nocheckout@main"
	seedExternalProject(t, f, noCheckout)
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+projects.HashPath(noCheckout)+"/file", map[string]any{"file": "README.md"}); rr.Code != http.StatusConflict {
		t.Errorf("external-no-checkout file = %d, want 409; body=%s", rr.Code, b)
	}

	// 3. External w/ checkout. Unauthorized bob (not shared) → 404.
	ext := "github.com/x/y@main"
	seedExternalProject(t, f, ext)
	writeCheckout(t, f, ext, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	extHash := projects.HashPath(ext)
	if rr, _ := doReq(t, f, bobCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "main.go"}); rr.Code != http.StatusNotFound {
		t.Errorf("bob unshared external = %d, want 404", rr.Code)
	}

	// 4a. Admin (privileged) → 200 with content.
	rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "main.go"})
	if rr.Code != http.StatusOK {
		t.Fatalf("admin external = %d, want 200; body=%s", rr.Code, body)
	}
	var fc openapi.FileContent
	if err := json.Unmarshal(body, &fc); err != nil {
		t.Fatalf("decode FileContent: %v", err)
	}
	if fc.Content != "package main\n\nfunc main() {}" {
		t.Errorf("content = %q", fc.Content)
	}
	if fc.TotalLines != 3 {
		t.Errorf("total_lines = %d, want 3", fc.TotalLines)
	}

	// 4b. Share to a group bob belongs to → bob now gets 200.
	_, gbody := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Readers"})
	var g struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(gbody, &g)
	bobID := userIDByEmail(t, f, adminCookie, "bob@example.com")
	doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", map[string]string{"user_id": bobID})
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/shares", map[string]string{"group_id": g.ID}); rr.Code != http.StatusNoContent {
		t.Fatalf("share = %d (%s)", rr.Code, b)
	}
	if rr, b := doReq(t, f, bobCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "main.go"}); rr.Code != http.StatusOK {
		t.Errorf("bob shared external = %d, want 200; body=%s", rr.Code, b)
	}
}

func userIDByEmail(t *testing.T, f *authTestFixture, adminCookie, email string) string {
	t.Helper()
	_, body := doReq(t, f, adminCookie, http.MethodGet, "/api/v1/admin/users", nil)
	var ul struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	_ = json.Unmarshal(body, &ul)
	for _, u := range ul.Users {
		if u.Email == email {
			return u.ID
		}
	}
	t.Fatalf("user %s not found", email)
	return ""
}

func TestReadProjectFile_PathTraversal(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	ext := "github.com/x/trav@main"
	seedExternalProject(t, f, ext)
	checkout := writeCheckout(t, f, ext, map[string]string{"ok.txt": "fine\n"})
	extHash := projects.HashPath(ext)

	// Lexical escapes / absolute paths / git metadata → 400. The git metadata
	// dir is hidden from /tree and must be unreadable via /file too.
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "a/../../b", "..", ".git/config", ".git"} {
		if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": bad}); rr.Code != http.StatusBadRequest {
			t.Errorf("traversal %q = %d, want 400; body=%s", bad, rr.Code, b)
		}
	}

	// Symlink escape: a symlink inside the checkout pointing outside it.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret\n"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(checkout, "evil")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "evil/secret.txt"}); rr.Code != http.StatusBadRequest {
		t.Errorf("symlink escape = %d, want 400; body=%s", rr.Code, b)
	}
}

func TestReadProjectFile_LineRange(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	ext := "github.com/x/lines@main"
	seedExternalProject(t, f, ext)
	writeCheckout(t, f, ext, map[string]string{"f.txt": "L1\nL2\nL3\nL4\nL5\n"})
	extHash := projects.HashPath(ext)

	read := func(reqBody map[string]any) openapi.FileContent {
		t.Helper()
		rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", reqBody)
		if rr.Code != http.StatusOK {
			t.Fatalf("read %v = %d; body=%s", reqBody, rr.Code, body)
		}
		var fc openapi.FileContent
		if err := json.Unmarshal(body, &fc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return fc
	}

	whole := read(map[string]any{"file": "f.txt"})
	if whole.Content != "L1\nL2\nL3\nL4\nL5" || whole.TotalLines != 5 || whole.StartLine != 1 || whole.EndLine != 5 {
		t.Errorf("whole = %+v", whole)
	}

	mid := read(map[string]any{"file": "f.txt", "start": 2, "end": 4})
	if mid.Content != "L2\nL3\nL4" || mid.StartLine != 2 || mid.EndLine != 4 || mid.TotalLines != 5 {
		t.Errorf("mid = %+v", mid)
	}

	// end past EOF clamps to total.
	tail := read(map[string]any{"file": "f.txt", "start": 4, "end": 99})
	if tail.Content != "L4\nL5" || tail.EndLine != 5 {
		t.Errorf("tail = %+v", tail)
	}

	// start past EOF → empty content, no error.
	past := read(map[string]any{"file": "f.txt", "start": 99})
	if past.Content != "" {
		t.Errorf("past = %+v", past)
	}
}

// TestReadProjectFile_RangePastByteCap covers a file larger than the 2 MiB read
// cap: a whole-file read returns truncated content, but a line range that
// begins past the readable portion is a 400 (the lines exist on disk but past
// what the server read) rather than a confusing empty 200.
func TestReadProjectFile_RangePastByteCap(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	ext := "github.com/x/big@main"
	seedExternalProject(t, f, ext)

	// ~2.4 MiB: 600 lines of 4096 bytes each. Only ~511 lines fit in the 2 MiB
	// the server reads, so a start beyond that is past the readable window.
	line := strings.Repeat("x", 4096)
	var sb strings.Builder
	for i := 0; i < 600; i++ {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	writeCheckout(t, f, ext, map[string]string{"big.txt": sb.String()})
	extHash := projects.HashPath(ext)

	// Whole-file read: truncated, but still 200 with content.
	rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "big.txt"})
	if rr.Code != http.StatusOK {
		t.Fatalf("whole big = %d; body=%s", rr.Code, body)
	}
	var fc openapi.FileContent
	if err := json.Unmarshal(body, &fc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !fc.Truncated || fc.Content == "" {
		t.Errorf("whole big should be truncated with content: %+v", fc.Truncated)
	}

	// Range beginning past the readable window → 400, not an empty 200.
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "big.txt", "start": 550}); rr.Code != http.StatusBadRequest {
		t.Errorf("range past byte cap = %d, want 400; body=%s", rr.Code, b)
	}
}

func TestListProjectTree(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	ext := "github.com/x/tree@main"
	seedExternalProject(t, f, ext)
	writeCheckout(t, f, ext, map[string]string{
		"README.md":     "# hi\n",
		"main.go":       "package main\n",
		"internal/a.go": "package internal\n",
		".git/config":   "[core]\n",
	})
	extHash := projects.HashPath(ext)

	// Root listing: dirs first, .git omitted.
	rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/tree", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("tree root = %d; body=%s", rr.Code, body)
	}
	var dl openapi.DirectoryListing
	if err := json.Unmarshal(body, &dl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := map[string]openapi.TreeEntryType{}
	for _, e := range dl.Entries {
		names[e.Name] = e.Type
		if e.Name == ".git" {
			t.Error(".git must be hidden at root")
		}
	}
	if names["internal"] != openapi.Dir {
		t.Errorf("internal should be a dir, got %v", names["internal"])
	}
	if names["main.go"] != openapi.File || names["README.md"] != openapi.File {
		t.Errorf("expected files missing: %+v", names)
	}

	// Subdirectory listing.
	rr, body = doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/tree", map[string]any{"dir": "internal"})
	if rr.Code != http.StatusOK {
		t.Fatalf("tree internal = %d; body=%s", rr.Code, body)
	}
	_ = json.Unmarshal(body, &dl)
	if len(dl.Entries) != 1 || dl.Entries[0].Name != "a.go" {
		t.Errorf("internal listing = %+v", dl.Entries)
	}
}

// TestReadProjectFile_Concurrent drives 5 concurrent readers against the file
// endpoint while a writer goroutine repeatedly rewrites the file NON-ATOMICALLY
// under the per-repo write-lock (mimicking git reset --hard). The read-lock in
// the handler must make every read observe a whole valid version — never a torn
// or half-written byte stream. Run with -race in CI.
func TestReadProjectFile_Concurrent(t *testing.T) {
	f := newFileFixture(t)
	adminCookie := adminLogin(t, f)
	ext := "github.com/x/conc@main"
	seedExternalProject(t, f, ext)
	extHash := projects.HashPath(ext)
	checkout := repocloner.LocalDirFor(f.Deps.DataDir, extHash)
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	target := filepath.Join(checkout, "f.txt")

	// Two valid full-file versions, large enough that a half-write is obvious.
	mk := func(tok string) string {
		var b strings.Builder
		for i := 0; i < 300; i++ {
			b.WriteString(tok)
			b.WriteString(tok)
			b.WriteString("\n")
		}
		return b.String()
	}
	versions := []string{mk("AAAA"), mk("BBBB")}
	if err := os.WriteFile(target, []byte(versions[0]), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// Returned content has the trailing newline stripped by sliceLines.
	valid := map[string]bool{
		strings.TrimSuffix(versions[0], "\n"): true,
		strings.TrimSuffix(versions[1], "\n"): true,
	}

	mu := f.Deps.RepoLocks.For(extHash)

	errCh := make(chan error, 16)
	stop := make(chan struct{})
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			mu.Lock()
			// Non-atomic rewrite: truncate, then write in two steps with a
			// scheduler yield between them. Without the lock this guarantees a
			// torn read.
			ff, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o644)
			if err == nil {
				v := versions[i%2]
				_, _ = ff.WriteString(v[:len(v)/2])
				runtime.Gosched()
				_, _ = ff.WriteString(v[len(v)/2:])
				_ = ff.Close()
			}
			mu.Unlock()
			runtime.Gosched()
		}
	}()

	var readerWG sync.WaitGroup
	for r := 0; r < 5; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for n := 0; n < 150; n++ {
				rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/file", map[string]any{"file": "f.txt"})
				if rr.Code != http.StatusOK {
					select {
					case errCh <- fmt.Errorf("status %d: %s", rr.Code, body):
					default:
					}
					return
				}
				var fc openapi.FileContent
				if err := json.Unmarshal(body, &fc); err != nil {
					select {
					case errCh <- fmt.Errorf("decode: %w", err):
					default:
					}
					return
				}
				if !valid[fc.Content] {
					select {
					case errCh <- fmt.Errorf("torn read: %d bytes not matching any whole version", len(fc.Content)):
					default:
					}
					return
				}
			}
		}()
	}

	readerWG.Wait()
	close(stop)
	writerWG.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}
