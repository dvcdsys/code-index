package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dvcdsys/code-index/server/internal/jobs"
)

// TestGetWebhookOrigin_PublicURL: when CIX_PUBLIC_URL is set and there is no
// tunnel, the origin endpoint reports source=public_url — i.e. the server is
// made public by infrastructure and a tunnel is NOT required. This is the
// case the dashboard must NOT present as a problem.
func TestGetWebhookOrigin_PublicURL(t *testing.T) {
	router, _ := reposRouter(t) // harness sets PublicBaseURL=https://cix.example.test, Tunnel nil

	rr := doJSON(t, router, http.MethodGet, "/api/v1/github/webhooks/origin", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Origin string `json:"origin"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "public_url" {
		t.Errorf("source = %q, want public_url", resp.Source)
	}
	if resp.Origin != "https://cix.example.test" {
		t.Errorf("origin = %q, want https://cix.example.test", resp.Origin)
	}
}

// TestGetWebhookOrigin_None: no tunnel and no CIX_PUBLIC_URL → source=none.
// This is the only state the dashboard should flag as a real problem.
func TestGetWebhookOrigin_None(t *testing.T) {
	d, err := dbOpenMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	router := NewRouter(Deps{
		DB:           d,
		AuthDisabled: true,
		Users:        seedlessUsers(d),
		Sessions:     seedlessSessions(d),
		APIKeys:      seedlessAPIKeys(d),
		Jobs:         jobs.New(d, jobs.Options{Concurrency: 1, PollEvery: time.Hour}),
		// PublicBaseURL deliberately unset; Tunnel nil.
	})

	rr := doJSON(t, router, http.MethodGet, "/api/v1/github/webhooks/origin", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Origin string `json:"origin"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Source != "none" {
		t.Errorf("source = %q, want none", resp.Source)
	}
	if resp.Origin != "" {
		t.Errorf("origin = %q, want empty", resp.Origin)
	}
}
