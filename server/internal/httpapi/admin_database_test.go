package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/dbmaint"
)

// databaseRoutes are the admin endpoints this feature adds. Every one of them
// exposes or changes the database as a whole, so none may be reachable by a
// non-admin.
var databaseRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/v1/admin/database"},
	{http.MethodPost, "/api/v1/admin/database/compact"},
	{http.MethodPost, "/api/v1/admin/database/reclaim"},
	{http.MethodPost, "/api/v1/admin/database/checkpoint"},
	{http.MethodPut, "/api/v1/admin/database/auto-vacuum"},
	{http.MethodGet, "/api/v1/admin/schedules"},
	{http.MethodPut, "/api/v1/admin/schedules/db.reclaim"},
}

func TestDatabaseEndpoints_RequireAdmin(t *testing.T) {
	f := newAdminFixture(t)
	viewer := viewerCookie(t, f)

	for _, rt := range databaseRoutes {
		rr, body := doReq(t, f.authTestFixture, viewer, rt.method, rt.path, nil)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d (%s), want 403",
				rt.method, rt.path, rr.Code, strings.TrimSpace(string(body)))
		}
	}
}

func TestDatabaseEndpoints_RejectAnonymous(t *testing.T) {
	f := newAdminFixture(t)
	for _, rt := range databaseRoutes {
		rr, _ := doReq(t, f.authTestFixture, "", rt.method, rt.path, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", rt.method, rt.path, rr.Code)
		}
	}
}

// The progress endpoint has to answer without a session, because it is what a
// dashboard polls while sessions cannot be written.
func TestMaintenanceStatus_IsPublicAndIdleWithNoJournal(t *testing.T) {
	f := newAdminFixture(t)
	rr, body := doReq(t, f.authTestFixture, "", http.MethodGet, "/maintenance/status", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("unauthenticated GET /maintenance/status = %d (%s), want 200", rr.Code, body)
	}
	var st dbmaint.State
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if st.Phase != dbmaint.PhaseIdle {
		t.Errorf("phase = %q with no journal on disk, want %q", st.Phase, dbmaint.PhaseIdle)
	}
}

// ---------------------------------------------------------------------------
// The write freeze
// ---------------------------------------------------------------------------

// The five search endpoints plus file and tree are POSTs that only read. A
// freeze that keyed off the HTTP method would refuse exactly the requests the
// feature promises to keep serving.
func TestFreeze_LetsReadOnlyPostsThrough(t *testing.T) {
	readOnly := []string{
		"/api/v1/projects/abc/search",
		"/api/v1/projects/abc/search/definitions",
		"/api/v1/projects/abc/search/files",
		"/api/v1/projects/abc/search/references",
		"/api/v1/projects/abc/search/symbols",
		"/api/v1/projects/abc/file",
		"/api/v1/projects/abc/tree",
	}
	for _, p := range readOnly {
		if !isReadOnlyRequest(http.MethodPost, p) {
			t.Errorf("POST %s is classified as a write; search and file reads must survive the freeze", p)
		}
	}
}

// The GitHub webhook is a write that skips authentication, which is why the
// gate is installed outside the auth middleware. If it were inside, webhook
// deliveries would write straight through a freeze.
func TestFreeze_RefusesTheUnauthenticatedWebhookWrite(t *testing.T) {
	if isReadOnlyRequest(http.MethodPost, "/api/v1/webhooks/github/deadbeef") {
		t.Fatal("the GitHub webhook is classified as read-only; it writes and must be refused while frozen")
	}
}

// Enumerate every mutating route the generated mux registers and assert each
// one is classified. This is the guard against a future endpoint silently
// inheriting whichever answer happens to be the default.
func TestFreeze_ClassifiesEveryGeneratedMutatingRoute(t *testing.T) {
	src, err := os.ReadFile("openapi/openapi.gen.go")
	if err != nil {
		t.Skipf("generated router not readable: %v", err)
	}
	re := regexp.MustCompile(`r\.(Post|Put|Patch|Delete)\(options\.BaseURL\+"([^"]+)"`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 50 {
		t.Fatalf("only found %d mutating routes; the pattern has stopped matching the generated file", len(matches))
	}

	// Concrete path stand-ins for chi's placeholders.
	placeholder := regexp.MustCompile(`\{[^}]+\}`)
	var allowed []string
	for _, m := range matches {
		method, pattern := m[1], m[2]
		path := placeholder.ReplaceAllString(pattern, "x")
		if isReadOnlyRequest(strings.ToUpper(method), path) {
			allowed = append(allowed, method+" "+pattern)
		}
	}

	// Exactly the seven read-only POSTs may pass. Anything else appearing
	// here means a write is about to be let through a freeze.
	want := map[string]bool{
		"Post /api/v1/projects/{path}/search":             true,
		"Post /api/v1/projects/{path}/search/definitions": true,
		"Post /api/v1/projects/{path}/search/files":       true,
		"Post /api/v1/projects/{path}/search/references":  true,
		"Post /api/v1/projects/{path}/search/symbols":     true,
		"Post /api/v1/projects/{path}/file":               true,
		"Post /api/v1/projects/{path}/tree":               true,
	}
	for _, got := range allowed {
		if !want[got] {
			t.Errorf("%s is allowed through the write freeze but is not a known read-only route", got)
		}
		delete(want, got)
	}
	for missing := range want {
		t.Errorf("%s is refused by the write freeze but only reads", missing)
	}
}

// A blocked write must say so in a way a client can act on, not just fail.
func TestFreeze_RefusedWriteCarriesRetryAfter(t *testing.T) {
	f := newAdminFixture(t)
	gate := &dbmaint.Gate{}
	f.Deps.DBMaint.Gate = gate
	router := NewRouter(f.Deps)
	admin := adminCookie(t, f)

	gate.Freeze()
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"host_path":"/tmp/x"}`)), admin)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("write while frozen = %d, want 503", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After on a refused write; clients have nothing to back off against")
	}
}

// The container healthcheck runs every 30s with 3 retries, and a restart
// policy acts on the result. If /health failed during a freeze we would kill
// our own compaction.
func TestHealth_StaysOKWhileFrozen(t *testing.T) {
	f := newAdminFixture(t)
	gate := &dbmaint.Gate{}
	f.Deps.DBMaint.Gate = gate
	router := NewRouter(f.Deps)

	gate.Freeze()
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/health while frozen = %d, want 200 — an unhealthy verdict here restarts the container mid-compaction", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["maintenance"] != true {
		t.Error("the payload does not say the server is in maintenance; a caller cannot tell this apart from normal health")
	}
}

// Reads must keep working while frozen — that is the entire justification for
// a read-only window rather than a shutdown.
func TestFreeze_ReadsStillServed(t *testing.T) {
	f := newAdminFixture(t)
	gate := &dbmaint.Gate{}
	f.Deps.DBMaint.Gate = gate
	router := NewRouter(f.Deps)
	admin := adminCookie(t, f)

	gate.Freeze()
	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil), admin)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/v1/projects while frozen = %d, want 200", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Compaction preflight
// ---------------------------------------------------------------------------

// A refusal must happen before anything is stopped. This pins the boundary:
// past the first Quiesce there is no way back except a restart, so every
// reason to say no has to be found first.
func TestCompact_RefusesWithJobsInFlightAndStopsNothing(t *testing.T) {
	f := newAdminFixture(t)
	quiesced := false
	restarted := false
	f.Deps.DBMaint = DBMaintHooks{
		Gate:           &dbmaint.Gate{},
		Quiesce:        func(ctx context.Context) error { quiesced = true; return nil },
		RequestRestart: func() { restarted = true },
	}
	if _, err := f.Deps.DB.Exec(`
		INSERT INTO jobs (id, type, status, dedupe_key, payload, scheduled_at, created_at)
		VALUES ('j1', 'index_repo', 'running', 'index:x', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	router := NewRouter(f.Deps)
	admin := adminCookie(t, f)

	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/database/compact", strings.NewReader(`{}`)), admin)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("compact with a running index job = %d (%s), want 409", rr.Code, rr.Body.String())
	}
	if quiesced {
		t.Error("background work was stopped before the request was refused")
	}
	if restarted {
		t.Error("a restart was requested for a compaction that never started")
	}
}

// The dashboard disables the Compact control on blocked_reason and renders it
// as the explanation. A field that is always null means the button is always
// enabled and the admin discovers the refusal from a toast after clicking.
func TestDatabaseState_ReportsWhyCompactionIsBlocked(t *testing.T) {
	f := newAdminFixture(t)
	f.Deps.DBMaint = DBMaintHooks{
		Gate:           &dbmaint.Gate{},
		Quiesce:        func(context.Context) error { return nil },
		RequestRestart: func() {},
	}
	router := NewRouter(f.Deps)
	admin := adminCookie(t, f)

	read := func() (*string, int) {
		t.Helper()
		req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/database", nil), admin)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var body struct {
			BlockedReason *string `json:"blocked_reason"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, rr.Body.String())
		}
		return body.BlockedReason, rr.Code
	}

	reason, code := read()
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/admin/database = %d", code)
	}
	if reason != nil {
		t.Fatalf("blocked_reason = %q on an idle server, want null", *reason)
	}

	if _, err := f.Deps.DB.Exec(`
		INSERT INTO jobs (id, type, status, dedupe_key, payload, scheduled_at, created_at)
		VALUES ('j1', 'index_repo', 'running', 'index:x', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	reason, _ = read()
	if reason == nil {
		t.Fatal("blocked_reason is null with a job in flight, but a compact request would be refused with 409")
	}
	if *reason == "" {
		t.Error("blocked_reason is present but empty; the dashboard renders it as the explanation")
	}
}
