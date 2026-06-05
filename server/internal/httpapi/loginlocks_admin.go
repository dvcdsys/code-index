package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Admin visibility + control over the in-memory login rate limiter
// (loginlimiter.go). The limiter blocks brute-force login attempts per IP and
// per (IP, email); both counters self-heal as their sliding windows expire,
// but a legitimate user who fat-fingered their password from a shared office
// IP can be stuck for the full 15-minute window. These endpoints let an admin
// see who is currently locked and lift a specific lock early.
//
// State is per-process and wiped on restart, so this is an operational
// convenience, not a persisted store — there is nothing to reconcile across
// replicas (the server runs single-instance) and no audit trail beyond the
// structured request log.

type loginLockPayload struct {
	Type      string `json:"type"`            // "ip" or "ip_email"
	IP        string `json:"ip"`              // source IP
	Email     string `json:"email,omitempty"` // present only for type "ip_email"
	Attempts  int    `json:"attempts"`        // attempts inside the window right now
	Limit     int    `json:"limit"`           // the cap that tripped the lock
	ExpiresAt string `json:"expires_at"`      // RFC3339 — when a slot next frees
}

// ListLoginLocks — GET /api/v1/admin/login-locks (admin only).
//
// Returns every limiter counter currently at or over its threshold, i.e. the
// IPs and (IP, email) pairs a login would be rejected for right now with 429.
func (s *Server) ListLoginLocks(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	locks := []loginLockPayload{}
	if s.loginLimiter != nil {
		for _, e := range s.loginLimiter.locks() {
			locks = append(locks, loginLockPayload{
				Type:      e.Type,
				IP:        e.IP,
				Email:     e.Email,
				Attempts:  e.Attempts,
				Limit:     e.Limit,
				ExpiresAt: e.ExpiresAt.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"locks": locks,
		"total": len(locks),
	})
}

// ResetLoginLock — POST /api/v1/admin/login-locks/reset (admin only).
//
// Clears one counter surfaced by ListLoginLocks. `type` selects the map:
// "ip" clears the per-IP horizontal-sweep counter; "ip_email" clears the
// per-(IP, email) counter for a single account. The admin picks the exact row
// to clear from the list, so we never guess across keys (clearing every IP
// for an email would need a map scan and could lift more than intended).
// Idempotent — clearing a key that is no longer locked is a no-op 204.
func (s *Server) ResetLoginLock(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Type  string `json:"type"`
		IP    string `json:"ip"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	body.IP = strings.TrimSpace(body.IP)
	if body.IP == "" {
		writeError(w, http.StatusUnprocessableEntity, "ip is required")
		return
	}
	if s.loginLimiter == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch body.Type {
	case "ip":
		s.loginLimiter.clearIP(body.IP)
	case "ip_email":
		if strings.TrimSpace(body.Email) == "" {
			writeError(w, http.StatusUnprocessableEntity, "email is required for type ip_email")
			return
		}
		s.loginLimiter.clearKey(body.IP, body.Email)
	default:
		writeError(w, http.StatusUnprocessableEntity, `type must be "ip" or "ip_email"`)
		return
	}
	// Clearing a lock weakens the brute-force defence, so record who did it
	// to what, in an explicit audit line — not only the generic request log.
	if s.Deps.Logger != nil {
		actorID, actorEmail := "", ""
		if ac != nil {
			actorID, actorEmail = ac.User.ID, ac.User.Email
		}
		s.Deps.Logger.Info("admin cleared login rate-limit lock",
			"actor_id", actorID,
			"actor_email", actorEmail,
			"lock_type", body.Type,
			"lock_ip", body.IP,
			"lock_email", body.Email,
		)
	}
	w.WriteHeader(http.StatusNoContent)
}
