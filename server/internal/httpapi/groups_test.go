package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/projects"
)

// seedUser creates a regular user via the admin API and returns a login cookie.
func seedUser(t *testing.T, f *authTestFixture, adminCookie, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"email": email, "initial_password": password, "role": "user",
	})
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(body)), adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed user %s status = %d (body=%s)", email, rr.Code, rr.Body.String())
	}
	return sessionCookie(loginRR(t, f.Router, email, password))
}

func doReq(t *testing.T, f *authTestFixture, cookie, method, path string, body any) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := withCookie(httptest.NewRequest(method, path, rdr), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.Router.ServeHTTP(rr, req)
	return rr, rr.Body.Bytes()
}

func TestGroupCRUD_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	userCookie := seedUser(t, f, adminCookie, "bob@example.com", "bobpass1234")

	// User is forbidden from every mutating group endpoint.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/groups"},
		{http.MethodGet, "/api/v1/groups/x"},
		{http.MethodPatch, "/api/v1/groups/x"},
		{http.MethodDelete, "/api/v1/groups/x"},
		{http.MethodGet, "/api/v1/groups/x/members"},
		{http.MethodPost, "/api/v1/groups/x/members"},
		{http.MethodDelete, "/api/v1/groups/x/members/y"},
	} {
		rr, _ := doReq(t, f, userCookie, c.method, c.path, map[string]any{"name": "n", "user_id": "u"})
		if rr.Code != http.StatusForbidden {
			t.Errorf("user %s %s = %d, want 403", c.method, c.path, rr.Code)
		}
	}

	// Admin lifecycle.
	rr, body := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Product"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create group = %d (%s)", rr.Code, body)
	}
	var g struct{ ID string `json:"id"` }
	_ = json.Unmarshal(body, &g)
	if g.ID == "" {
		t.Fatal("group id empty")
	}

	if rr, _ := doReq(t, f, adminCookie, http.MethodGet, "/api/v1/groups/"+g.ID, nil); rr.Code != http.StatusOK {
		t.Errorf("get group = %d", rr.Code)
	}
	if rr, _ := doReq(t, f, adminCookie, http.MethodPatch, "/api/v1/groups/"+g.ID, map[string]string{"description": "PM agents"}); rr.Code != http.StatusOK {
		t.Errorf("patch group = %d", rr.Code)
	}

	// Add bob (need his id — look him up via admin users list).
	rr, body = doReq(t, f, adminCookie, http.MethodGet, "/api/v1/admin/users", nil)
	var ul struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	_ = json.Unmarshal(body, &ul)
	var bobID string
	for _, u := range ul.Users {
		if u.Email == "bob@example.com" {
			bobID = u.ID
		}
	}
	if bobID == "" {
		t.Fatal("bob id not found")
	}
	if rr, _ := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", map[string]string{"user_id": bobID}); rr.Code != http.StatusNoContent {
		t.Errorf("add member = %d", rr.Code)
	}
	// Adding a non-existent user → 422.
	if rr, _ := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", map[string]string{"user_id": "nope"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("add bad member = %d, want 422", rr.Code)
	}

	rr, body = doReq(t, f, adminCookie, http.MethodGet, "/api/v1/groups/"+g.ID+"/members", nil)
	var ml struct{ Total int `json:"total"` }
	_ = json.Unmarshal(body, &ml)
	if rr.Code != http.StatusOK || ml.Total != 1 {
		t.Errorf("list members = %d total=%d", rr.Code, ml.Total)
	}

	// bob now sees the group via /auth/me and GET /groups (member-scoped).
	rr, body = doReq(t, f, userCookie, http.MethodGet, "/api/v1/auth/me", nil)
	var me struct {
		Groups []struct{ ID string `json:"id"` } `json:"groups"`
	}
	_ = json.Unmarshal(body, &me)
	if rr.Code != http.StatusOK || len(me.Groups) != 1 || me.Groups[0].ID != g.ID {
		t.Errorf("/auth/me groups = %+v (status %d)", me.Groups, rr.Code)
	}

	// Remove + delete.
	if rr, _ := doReq(t, f, adminCookie, http.MethodDelete, "/api/v1/groups/"+g.ID+"/members/"+bobID, nil); rr.Code != http.StatusNoContent {
		t.Errorf("remove member = %d", rr.Code)
	}
	if rr, _ := doReq(t, f, adminCookie, http.MethodDelete, "/api/v1/groups/"+g.ID, nil); rr.Code != http.StatusNoContent {
		t.Errorf("delete group = %d", rr.Code)
	}
}

func TestProjectShareAndOwnerReassign(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))
	aliceCookie := seedUser(t, f, adminCookie, "alice@example.com", "alicepass1")

	// Find alice's id.
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

	// Insert an external project (project row + git_repos peer) directly.
	extPath := "github.com/x/y@main"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: extPath}); err != nil {
		t.Fatalf("create external project: %v", err)
	}
	if _, err := f.Deps.DB.ExecContext(t.Context(),
		`INSERT INTO git_repos (project_path, github_url, branch, webhook_secret, created_at, updated_at)
		 VALUES (?, 'https://github.com/x/y', 'main', 's', '2024-01-01', '2024-01-01')`, extPath); err != nil {
		t.Fatalf("insert git_repos: %v", err)
	}
	extHash := projects.HashPath(extPath)

	// Group with alice as member.
	_, gbody := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Product"})
	var g struct{ ID string `json:"id"` }
	_ = json.Unmarshal(gbody, &g)
	doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", map[string]string{"user_id": aliceID})

	// Before share: alice does not see the external project.
	if !aliceSeesProject(t, f, aliceCookie, extHash) {
		// expected: not visible
	} else {
		t.Error("alice should NOT see the external project before share")
	}

	// Share to the group (admin). Then alice sees it.
	if rr, b := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/projects/"+extHash+"/shares", map[string]string{"group_id": g.ID}); rr.Code != http.StatusNoContent {
		t.Fatalf("share project = %d (%s)", rr.Code, b)
	}
	if !aliceSeesProject(t, f, aliceCookie, extHash) {
		t.Error("alice should see the external project after share")
	}

	// Reassign owner of an external project → 422.
	if rr, _ := doReq(t, f, adminCookie, http.MethodPut, "/api/v1/projects/"+extHash+"/owner", map[string]string{"owner_user_id": aliceID}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("reassign external owner = %d, want 422", rr.Code)
	}

	// Local project owned by admin → reassign to alice → alice now sees it.
	localPath := "/tmp/admin-local"
	if _, err := projects.Create(t.Context(), f.Deps.DB, projects.CreateRequest{HostPath: localPath, OwnerUserID: f.UserID}); err != nil {
		t.Fatalf("create local project: %v", err)
	}
	localHash := projects.HashPath(localPath)
	if aliceSeesProject(t, f, aliceCookie, localHash) {
		t.Error("alice should not see admin's local project before reassign")
	}
	if rr, b := doReq(t, f, adminCookie, http.MethodPut, "/api/v1/projects/"+localHash+"/owner", map[string]string{"owner_user_id": aliceID}); rr.Code != http.StatusOK {
		t.Fatalf("reassign local owner = %d (%s)", rr.Code, b)
	}
	if !aliceSeesProject(t, f, aliceCookie, localHash) {
		t.Error("alice should see the local project after reassign")
	}
}

func aliceSeesProject(t *testing.T, f *authTestFixture, cookie, hash string) bool {
	t.Helper()
	_, body := doReq(t, f, cookie, http.MethodGet, "/api/v1/projects", nil)
	var pl struct {
		Projects []struct{ PathHash string `json:"path_hash"` } `json:"projects"`
	}
	_ = json.Unmarshal(body, &pl)
	for _, p := range pl.Projects {
		if p.PathHash == hash {
			return true
		}
	}
	return false
}

func TestWorkspaceShare_OwnerInGroupOnly(t *testing.T) {
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

	// Two groups: gIn (alice is a member), gOut (she is not).
	_, b1 := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "in"})
	_, b2 := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups", map[string]string{"name": "out"})
	var gIn, gOut struct{ ID string `json:"id"` }
	_ = json.Unmarshal(b1, &gIn)
	_ = json.Unmarshal(b2, &gOut)
	doReq(t, f, adminCookie, http.MethodPost, "/api/v1/groups/"+gIn.ID+"/members", map[string]string{"user_id": aliceID})

	// Alice creates a workspace (she owns it).
	rr, wbody := doReq(t, f, aliceCookie, http.MethodPost, "/api/v1/workspaces", map[string]string{"name": "alice-ws"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create workspace = %d (%s)", rr.Code, wbody)
	}
	var ws struct{ ID string `json:"id"` }
	_ = json.Unmarshal(wbody, &ws)

	// Share to a group she belongs to → 204.
	if rr, b := doReq(t, f, aliceCookie, http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/shares", map[string]string{"group_id": gIn.ID}); rr.Code != http.StatusNoContent {
		t.Errorf("share to own group = %d (%s)", rr.Code, b)
	}
	// Share to a group she does NOT belong to → 403.
	if rr, _ := doReq(t, f, aliceCookie, http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/shares", map[string]string{"group_id": gOut.ID}); rr.Code != http.StatusForbidden {
		t.Errorf("share to foreign group = %d, want 403", rr.Code)
	}
	// Admin may share to any group.
	if rr, _ := doReq(t, f, adminCookie, http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/shares", map[string]string{"group_id": gOut.ID}); rr.Code != http.StatusNoContent {
		t.Errorf("admin share to any group = %d", rr.Code)
	}
}
