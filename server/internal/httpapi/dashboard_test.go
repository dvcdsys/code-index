package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dashboardServer wires the router with auth disabled so the dashboard
// routes can be hit directly. Auth would only catch the API calls anyway —
// /dashboard/* is a public path by design.
func dashboardServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := Deps{AuthDisabled: true}
	srv := httptest.NewServer(NewRouter(d))
	t.Cleanup(srv.Close)
	return srv
}

func TestDashboard_IndexServesHTML(t *testing.T) {
	srv := dashboardServer(t)
	resp, err := http.Get(srv.URL + "/dashboard")
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html…", ct)
	}
}

func TestDashboard_HistoryFallback(t *testing.T) {
	// Deep links into the SPA must return the same HTML shell so the
	// browser can boot React Router and route client-side. Anything
	// without a file extension counts as an in-app route.
	srv := dashboardServer(t)
	for _, path := range []string{
		"/dashboard/projects",
		"/dashboard/projects/abc123",
		"/dashboard/some/deep/route",
	} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (history fallback)", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("Content-Type = %q, want text/html… (history fallback)", ct)
			}
		})
	}
}

func TestDashboard_MissingAsset404(t *testing.T) {
	// A path that LOOKS like a file (has an extension) and isn't in the
	// embed should 404 rather than fall through to index.html — otherwise
	// the SPA would silently absorb /dashboard/foo.js requests.
	srv := dashboardServer(t)
	resp, err := http.Get(srv.URL + "/dashboard/no-such-file.js")
	if err != nil {
		t.Fatalf("GET missing asset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDashboard_PlaceholderConstantPresent(t *testing.T) {
	// The inline placeholder (dashboardPlaceholderHTML in dashboard.go) is
	// the fallback when dist/index.html is missing — typically because the
	// operator forgot to run `make dashboard-build` before `go build`. We
	// can't easily exercise the missing-index path in unit tests (the
	// embed.FS is sealed at compile time), but we CAN sanity-check the
	// constant itself so a refactor never silently empties it.
	if !strings.Contains(dashboardPlaceholderHTML, "make dashboard-build") {
		t.Fatal("placeholder HTML must mention `make dashboard-build` so the operator knows what to do")
	}
	if !strings.Contains(dashboardPlaceholderHTML, "<html") {
		t.Fatal("placeholder HTML must look like HTML")
	}
}

func TestDashboard_PathBypassesAuth(t *testing.T) {
	// isPublicPath must include /dashboard and /dashboard/* — otherwise
	// the SPA shell would 401 before it could even render the login form.
	if !isPublicPath("/dashboard") {
		t.Fatal("/dashboard not in public paths")
	}
	if !isPublicPath("/dashboard/assets/index-abc.js") {
		t.Fatal("/dashboard/assets/* not in public paths")
	}
	if !isPublicPath("/dashboard/projects/foo") {
		t.Fatal("/dashboard/<route> not in public paths")
	}
	// Sanity — API paths stay gated.
	if isPublicPath("/api/v1/projects") {
		t.Fatal("/api/v1/projects must NOT be public")
	}
}
