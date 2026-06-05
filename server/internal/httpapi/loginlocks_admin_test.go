package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dvcdsys/code-index/server/internal/users"
)

// loginLocksRR issues GET /api/v1/admin/login-locks as the given session.
func loginLocksRR(t *testing.T, router http.Handler, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := withCookie(httptest.NewRequest(http.MethodGet, "/api/v1/admin/login-locks", nil), cookie)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// resetLoginLockRR issues POST /api/v1/admin/login-locks/reset with body.
func resetLoginLockRR(t *testing.T, router http.Handler, cookie string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/v1/admin/login-locks/reset", bytes.NewReader(raw)), cookie)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

type loginLockListBody struct {
	Locks []struct {
		Type     string `json:"type"`
		IP       string `json:"ip"`
		Email    string `json:"email"`
		Attempts int    `json:"attempts"`
		Limit    int    `json:"limit"`
	} `json:"locks"`
	Total int `json:"total"`
}

func TestLoginLocks_AdminOnly(t *testing.T) {
	f := newAuthFixture(t)

	if _, err := f.Deps.Users.Create(context.Background(), "viewer@example.com", "viewerpass1", users.RoleUser, false); err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	viewerCookie := sessionCookie(loginRR(t, f.Router, "viewer@example.com", "viewerpass1"))

	if rr := loginLocksRR(t, f.Router, viewerCookie); rr.Code != http.StatusForbidden {
		t.Errorf("viewer GET login-locks status = %d, want 403", rr.Code)
	}
	rr := resetLoginLockRR(t, f.Router, viewerCookie, map[string]string{"type": "ip", "ip": "1.2.3.4"})
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer reset status = %d, want 403", rr.Code)
	}
}

func TestLoginLocks_ListAndReset(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	// Drive the per-(IP, email) counter to its limit with bad passwords. The
	// default httptest RemoteAddr is 192.0.2.1, so every attempt shares one IP.
	for range 5 {
		if rr := loginRR(t, f.Router, "admin@example.com", "WRONG"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("warmup attempt status = %d, want 401", rr.Code)
		}
	}
	// The lock is now active: a 6th attempt (even with the right password) 429s.
	if rr := loginRR(t, f.Router, "admin@example.com", "secret-password"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("post-limit status = %d, want 429", rr.Code)
	}

	// The admin can see exactly one ip_email lock for that IP/email.
	listRR := loginLocksRR(t, f.Router, adminCookie)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d (body=%s)", listRR.Code, listRR.Body.String())
	}
	var list loginLockListBody
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 1 || len(list.Locks) != 1 {
		t.Fatalf("total = %d, locks = %d, want 1/1 (body=%s)", list.Total, len(list.Locks), listRR.Body.String())
	}
	lock := list.Locks[0]
	if lock.Type != "ip_email" || lock.IP != "192.0.2.1" || lock.Email != "admin@example.com" {
		t.Errorf("lock = %+v, want type ip_email ip 192.0.2.1 email admin@example.com", lock)
	}
	if lock.Attempts < lock.Limit {
		t.Errorf("attempts %d < limit %d — should be at or over limit", lock.Attempts, lock.Limit)
	}

	// Admin clears that exact row.
	if rr := resetLoginLockRR(t, f.Router, adminCookie, map[string]string{
		"type": "ip_email", "ip": "192.0.2.1", "email": "admin@example.com",
	}); rr.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d (body=%s), want 204", rr.Code, rr.Body.String())
	}

	// The lock is gone from the list and login is no longer throttled (a wrong
	// password now reaches the authenticator and returns 401, not 429).
	if rr := loginLocksRR(t, f.Router, adminCookie); rr.Code == http.StatusOK {
		var after loginLockListBody
		_ = json.Unmarshal(rr.Body.Bytes(), &after)
		if after.Total != 0 {
			t.Errorf("post-reset total = %d, want 0", after.Total)
		}
	}
	if rr := loginRR(t, f.Router, "admin@example.com", "WRONG"); rr.Code != http.StatusUnauthorized {
		t.Errorf("post-reset login status = %d, want 401 (limiter cleared)", rr.Code)
	}
}

func TestLoginLocks_ResetValidation(t *testing.T) {
	f := newAuthFixture(t)
	adminCookie := sessionCookie(loginRR(t, f.Router, "admin@example.com", "secret-password"))

	cases := []struct {
		name string
		body map[string]string
		want int
	}{
		{"missing ip", map[string]string{"type": "ip"}, http.StatusUnprocessableEntity},
		{"bad type", map[string]string{"type": "nonsense", "ip": "1.2.3.4"}, http.StatusUnprocessableEntity},
		{"ip_email without email", map[string]string{"type": "ip_email", "ip": "1.2.3.4"}, http.StatusUnprocessableEntity},
		{"ip ok (idempotent no-op)", map[string]string{"type": "ip", "ip": "9.9.9.9"}, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := resetLoginLockRR(t, f.Router, adminCookie, tc.body)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}
