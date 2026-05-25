package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// TestListProjectWorkspaces_AccessAndVisibility verifies the access-checked
// version of GET /api/v1/projects/{hash}/workspaces. Prior to the fix the
// handler returned 200 with the full workspace membership list for any
// existing project hash, regardless of caller — leaking workspace ids and
// names of resources the caller had no business seeing. The fix:
//
//  1. Gate on requireProjectAccess (admin / owner / shared-via-group), so
//     foreign hashes return 404 instead of 200.
//  2. For non-admin callers, filter the result to workspaces the caller can
//     also see — owning the project does NOT entitle you to learn what
//     OTHER private workspaces it is linked into.
func TestListProjectWorkspaces_AccessAndVisibility(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	aliceCookie := seedUser(t, f, adminCookie, "alice@example.com", "alicepass1")

	// Resolve alice's id via /admin/users (fixture pattern).
	_, body := doReq(t, f, adminCookie, http.MethodGet, "/api/v1/admin/users", nil)
	var ul struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	_ = json.Unmarshal(body, &ul)
	var aliceID string
	for _, u := range ul.Users {
		if u.Email == "alice@example.com" {
			aliceID = u.ID
		}
	}
	if aliceID == "" {
		t.Fatal("could not resolve alice user id")
	}

	// Alice owns a local project.
	aliceProjectPath := "/tmp/alice-proj"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{
		HostPath:    aliceProjectPath,
		OwnerUserID: aliceID,
	}); err != nil {
		t.Fatalf("create alice project: %v", err)
	}
	if _, err := f.Deps.DB.ExecContext(t.Context(),
		`UPDATE projects SET status='indexed' WHERE host_path=?`, aliceProjectPath); err != nil {
		t.Fatalf("mark project indexed: %v", err)
	}
	aliceProjectHash := projects.HashPath(aliceProjectPath)

	// Admin owns an unrelated project — used to verify alice gets a 404 (not
	// a 200 with empty list) when probing a hash she has no access to.
	adminProjectPath := "/tmp/admin-only-proj"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{
		HostPath:    adminProjectPath,
		OwnerUserID: f.UserID,
	}); err != nil {
		t.Fatalf("create admin project: %v", err)
	}
	adminProjectHash := projects.HashPath(adminProjectPath)

	// Two workspaces own alice's project:
	//   alice-ws  — owned by alice (visible to her)
	//   admin-ws  — owned by admin (NOT visible to alice)
	// Both workspaces link alice's project. Before the fix, alice could
	// enumerate admin-ws by name + id through this endpoint.
	aliceWS, err := f.Deps.Workspaces.CreateOwned(t.Context(), "alice-ws", "", aliceID)
	if err != nil {
		t.Fatalf("create alice workspace: %v", err)
	}
	adminWS, err := f.Deps.Workspaces.CreateOwned(t.Context(), "admin-ws", "", f.UserID)
	if err != nil {
		t.Fatalf("create admin workspace: %v", err)
	}
	// Link directly via SQL — the test fixture doesn't wire the
	// workspaceprojects.Service, and the linker would otherwise reject
	// us anyway because it checks that the workspaces feature is enabled.
	// We just need the rows present so the handler's SELECT returns them.
	for _, wsID := range []string{aliceWS.ID, adminWS.ID} {
		if _, err := f.Deps.DB.ExecContext(t.Context(),
			`INSERT INTO workspace_projects (workspace_id, project_path, added_at)
			 VALUES (?, ?, '2024-01-01T00:00:00Z')`,
			wsID, aliceProjectPath); err != nil {
			t.Fatalf("link ws=%s: %v", wsID, err)
		}
	}

	// 1) Alice probes admin's project — must be 404 (hide existence).
	rr, _ := doReq(t, f, aliceCookie, http.MethodGet,
		"/api/v1/projects/"+adminProjectHash+"/workspaces", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("alice on foreign project hash = %d, want 404 (was a 200 leak before the fix)", rr.Code)
	}

	// 2) Alice probes her OWN project — sees ONLY alice-ws, NOT admin-ws.
	rr, body = doReq(t, f, aliceCookie, http.MethodGet,
		"/api/v1/projects/"+aliceProjectHash+"/workspaces", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alice on own project = %d (%s), want 200", rr.Code, body)
	}
	var resp struct {
		Workspaces []struct {
			WorkspaceID   string `json:"workspace_id"`
			WorkspaceName string `json:"workspace_name"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Workspaces) != 1 {
		t.Fatalf("alice saw %d workspaces (%+v), want 1 (alice-ws only)", len(resp.Workspaces), resp.Workspaces)
	}
	if resp.Workspaces[0].WorkspaceID != aliceWS.ID {
		t.Errorf("alice saw workspace id %q, want %q", resp.Workspaces[0].WorkspaceID, aliceWS.ID)
	}
	for _, w := range resp.Workspaces {
		if w.WorkspaceID == adminWS.ID || w.WorkspaceName == "admin-ws" {
			t.Errorf("alice leaked admin-ws (id=%s name=%s)", w.WorkspaceID, w.WorkspaceName)
		}
	}

	// 3) Admin sees both workspaces — no filtering.
	rr, body = doReq(t, f, adminCookie, http.MethodGet,
		"/api/v1/projects/"+aliceProjectHash+"/workspaces", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin on alice's project = %d (%s), want 200", rr.Code, body)
	}
	_ = json.Unmarshal(body, &resp)
	if len(resp.Workspaces) != 2 {
		t.Errorf("admin saw %d workspaces, want 2", len(resp.Workspaces))
	}
}

// TestIndexStatus_GroupMemberHasReadAccess: prior to the fix the index
// status endpoint used requireProjectOwnership — a user with READ access
// to a project (via a group share) could /search the indexed content but
// would get 403 on /index/status, blocking the dashboard from showing an
// "indexing…" badge for read-only users. After the fix the gate is
// requireProjectAccess, matching /search and /summary.
func TestIndexStatus_GroupMemberHasReadAccess(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	aliceCookie := seedUser(t, f, adminCookie, "alice@example.com", "alicepass1")

	_, body := doReq(t, f, adminCookie, http.MethodGet, "/api/v1/admin/users", nil)
	var ul struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	_ = json.Unmarshal(body, &ul)
	var aliceID string
	for _, u := range ul.Users {
		if u.Email == "alice@example.com" {
			aliceID = u.ID
		}
	}

	// External project (ownerless) — only path that lets us test the
	// group-share branch. Shared to a group alice belongs to.
	extPath := "github.com/acme/shared@main"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: extPath}); err != nil {
		t.Fatalf("create external project: %v", err)
	}
	if _, err := f.Deps.DB.ExecContext(t.Context(),
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES (?, 'https://github.com/acme/shared', 'main', 's', '2024-01-01', '2024-01-01')`, extPath); err != nil {
		t.Fatalf("insert git_repos: %v", err)
	}
	extHash := projects.HashPath(extPath)

	// Group + share + membership.
	_, gbody := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Eng"})
	var g struct{ ID string `json:"id"` }
	_ = json.Unmarshal(gbody, &g)
	doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", map[string]string{"user_id": aliceID})
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/shares", map[string]string{"group_id": g.ID}); rr.Code != http.StatusNoContent {
		t.Fatalf("share project = %d (%s)", rr.Code, b)
	}

	// Foreign project alice has NO access to — must remain 404.
	foreignPath := "github.com/acme/secret@main"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: foreignPath}); err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	if _, err := f.Deps.DB.ExecContext(t.Context(),
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES (?, 'https://github.com/acme/secret', 'main', 's', '2024-01-01', '2024-01-01')`, foreignPath); err != nil {
		t.Fatalf("insert foreign git_repos: %v", err)
	}
	foreignHash := projects.HashPath(foreignPath)

	t.Run("alice reads /index/status on shared project", func(t *testing.T) {
		rr, body := doReq(t, f, aliceCookie, http.MethodGet,
			"/api/v1/projects/"+extHash+"/index/status", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("alice GET /index/status on shared project = %d (%s), want 200 (was 403 before the fix)", rr.Code, body)
		}
	})

	t.Run("alice gets 404 on foreign project /index/status", func(t *testing.T) {
		rr, _ := doReq(t, f, aliceCookie, http.MethodGet,
			"/api/v1/projects/"+foreignHash+"/index/status", nil)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("alice GET /index/status on foreign project = %d, want 404", rr.Code)
		}
	})

	t.Run("admin always sees /index/status", func(t *testing.T) {
		rr, _ := doReq(t, f, adminCookie, http.MethodGet,
			"/api/v1/projects/"+extHash+"/index/status", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin GET /index/status = %d, want 200", rr.Code)
		}
	})
}
