package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/server/internal/apikeys"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/sessions"
	"github.com/dvcdsys/code-index/server/internal/users"
)

// userPayload mirrors the OpenAPI `User` schema. Built by hand instead of
// using the generated openapi.User to keep date formatting under our
// control (RFC3339Nano UTC) — keeps wire output stable across Go versions.
type userPayload struct {
	ID                 string  `json:"id"`
	Email              string  `json:"email"`
	Role               string  `json:"role"`
	MustChangePassword bool    `json:"must_change_password"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	Disabled           bool    `json:"disabled"`
	DisabledAt         *string `json:"disabled_at"`
}

func userToPayload(u users.User) userPayload {
	p := userPayload{
		ID:                 u.ID,
		Email:              u.Email,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:          u.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Disabled:           u.DisabledAt != nil,
	}
	if u.DisabledAt != nil {
		s := u.DisabledAt.UTC().Format(time.RFC3339Nano)
		p.DisabledAt = &s
	}
	return p
}

// userWithStatsPayload mirrors the OpenAPI `UserWithStats` schema. Returned
// only by the admin /users list endpoint — keeps the per-request /auth/me
// shape free of N+1 aggregate columns.
type userWithStatsPayload struct {
	userPayload
	LastLoginAt         *string `json:"last_login_at"`
	ActiveSessionsCount int     `json:"active_sessions_count"`
	APIKeysCount        int     `json:"api_keys_count"`
}

func userWithStatsToPayload(u users.UserWithStats) userWithStatsPayload {
	p := userWithStatsPayload{
		userPayload:         userToPayload(u.User),
		ActiveSessionsCount: u.ActiveSessionsCount,
		APIKeysCount:        u.APIKeysCount,
	}
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.UTC().Format(time.RFC3339Nano)
		p.LastLoginAt = &s
	}
	return p
}

type sessionPayload struct {
	ID         string  `json:"id"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  string  `json:"expires_at"`
	LastSeenAt string  `json:"last_seen_at"`
	LastSeenIP *string `json:"last_seen_ip"`
	LastSeenUA *string `json:"last_seen_ua"`
	IsCurrent  bool    `json:"is_current"`
}

func sessionToPayload(s sessions.Session, currentID string) sessionPayload {
	p := sessionPayload{
		ID:         s.ID,
		CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:  s.ExpiresAt.UTC().Format(time.RFC3339Nano),
		LastSeenAt: s.LastSeenAt.UTC().Format(time.RFC3339Nano),
		IsCurrent:  s.ID == currentID,
	}
	if s.LastSeenIP != "" {
		p.LastSeenIP = &s.LastSeenIP
	}
	if s.LastSeenUA != "" {
		p.LastSeenUA = &s.LastSeenUA
	}
	return p
}

type apiKeyPayload struct {
	ID          string  `json:"id"`
	OwnerUserID string  `json:"owner_user_id"`
	Name        string  `json:"name"`
	Prefix      string  `json:"prefix"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at"`
	LastUsedIP  *string `json:"last_used_ip"`
	LastUsedUA  *string `json:"last_used_ua"`
	Revoked     bool    `json:"revoked"`
	RevokedAt   *string `json:"revoked_at"`
}

func apiKeyToPayload(k apikeys.ApiKey) apiKeyPayload {
	p := apiKeyPayload{
		ID:          k.ID,
		OwnerUserID: k.OwnerUserID,
		Name:        k.Name,
		Prefix:      k.Prefix,
		CreatedAt:   k.CreatedAt.UTC().Format(time.RFC3339Nano),
		Revoked:     k.RevokedAt != nil,
	}
	if k.LastUsedAt != nil {
		s := k.LastUsedAt.UTC().Format(time.RFC3339Nano)
		p.LastUsedAt = &s
	}
	if k.LastUsedIP != "" {
		p.LastUsedIP = &k.LastUsedIP
	}
	if k.LastUsedUA != "" {
		p.LastUsedUA = &k.LastUsedUA
	}
	if k.RevokedAt != nil {
		s := k.RevokedAt.UTC().Format(time.RFC3339Nano)
		p.RevokedAt = &s
	}
	return p
}

// ---------------------------------------------------------------------------
// Auth endpoints
// ---------------------------------------------------------------------------

// GetBootstrapStatus — GET /api/v1/auth/bootstrap-status (public).
func (s *Server) GetBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Users == nil {
		writeJSON(w, http.StatusOK, map[string]any{"needs_bootstrap": false})
		return
	}
	n, err := s.Deps.Users.Count(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check bootstrap status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"needs_bootstrap": n == 0})
}

// Login — POST /api/v1/auth/login (public).
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Users == nil || s.Deps.Sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "auth not configured")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "email and password are required")
		return
	}
	u, err := s.Deps.Users.Authenticate(r.Context(), body.Email, body.Password)
	if err != nil {
		switch {
		case errors.Is(err, users.ErrInvalidLogin):
			writeError(w, http.StatusUnauthorized, "Invalid email or password")
		case errors.Is(err, users.ErrUserDisabled):
			writeError(w, http.StatusUnauthorized, "Account is disabled")
		default:
			writeError(w, http.StatusInternalServerError, "login failed")
		}
		return
	}
	sess, err := s.Deps.Sessions.Create(r.Context(), u.ID, clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	setSessionCookie(w, r, sess.ID, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{"user": userToPayload(u)})
}

// Logout — POST /api/v1/auth/logout.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	if ac.Method == "session" && ac.Session != nil && s.Deps.Sessions != nil {
		_ = s.Deps.Sessions.Delete(r.Context(), ac.Session.ID)
		clearSessionCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMe — GET /api/v1/auth/me.
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        userToPayload(ac.User),
		"auth_method": ac.Method,
	})
}

// ChangePassword — POST /api/v1/auth/change-password.
//
// Verifies the current password, updates to the new one, and revokes
// every other session of the user (the cookie carrying THIS request is
// preserved so the user stays logged in on the current device).
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.NewPassword == "" || len(body.NewPassword) < 8 {
		writeError(w, http.StatusUnprocessableEntity, "new password must be at least 8 characters")
		return
	}
	// Re-authenticate with the current password to prove possession.
	if _, err := s.Deps.Users.Authenticate(r.Context(), ac.User.Email, body.CurrentPassword); err != nil {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := s.Deps.Users.UpdatePassword(r.Context(), ac.User.ID, body.NewPassword); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	// Revoke every OTHER session — the current one stays. Best-effort:
	// failure here is non-fatal (the new password is already set).
	if ac.Session != nil {
		_ = s.Deps.Sessions.DeleteAllForUserExcept(r.Context(), ac.User.ID, ac.Session.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMySessions — GET /api/v1/auth/sessions.
func (s *Server) ListMySessions(w http.ResponseWriter, r *http.Request) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	list, err := s.Deps.Sessions.ListForUser(r.Context(), ac.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	currentID := ""
	if ac.Session != nil {
		currentID = ac.Session.ID
	}
	out := make([]sessionPayload, 0, len(list))
	for _, s := range list {
		out = append(out, sessionToPayload(s, currentID))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"total":    len(out),
	})
}

// DeleteMySession — DELETE /api/v1/auth/sessions/{id}.
func (s *Server) DeleteMySession(w http.ResponseWriter, r *http.Request, id string) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	// Confirm the session belongs to the caller — otherwise pretend it
	// doesn't exist (404) so a user cannot enumerate other people's
	// session ids.
	list, err := s.Deps.Sessions.ListForUser(r.Context(), ac.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	owns := false
	for _, s := range list {
		if s.ID == id {
			owns = true
			break
		}
	}
	if !owns {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	_ = s.Deps.Sessions.Delete(r.Context(), id)
	if ac.Session != nil && ac.Session.ID == id {
		clearSessionCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Admin user endpoints
// ---------------------------------------------------------------------------

func mustBeAdmin(w http.ResponseWriter, r *http.Request) (*authContext, bool) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return nil, false
	}
	if ac.User.Role != users.RoleAdmin {
		writeError(w, http.StatusForbidden, "This action requires role: admin")
		return nil, false
	}
	return ac, true
}

// ListUsers — GET /api/v1/admin/users (admin only).
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := mustBeAdmin(w, r); !ok {
		return
	}
	list, err := s.Deps.Users.ListWithStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	out := make([]userWithStatsPayload, 0, len(list))
	for _, u := range list {
		out = append(out, userWithStatsToPayload(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": out,
		"total": len(out),
	})
}

// CreateUser — POST /api/v1/admin/users (admin only).
func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := mustBeAdmin(w, r); !ok {
		return
	}
	var body struct {
		Email           string `json:"email"`
		InitialPassword string `json:"initial_password"`
		Role            string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || len(body.InitialPassword) < 8 {
		writeError(w, http.StatusUnprocessableEntity, "email and initial_password (>= 8 chars) are required")
		return
	}
	u, err := s.Deps.Users.Create(r.Context(), body.Email, body.InitialPassword, body.Role, true)
	if err != nil {
		switch {
		case errors.Is(err, users.ErrEmailTaken):
			writeError(w, http.StatusConflict, "email already in use")
		case errors.Is(err, users.ErrInvalidRole):
			writeError(w, http.StatusUnprocessableEntity, "role must be 'admin' or 'viewer'")
		default:
			writeError(w, http.StatusInternalServerError, "could not create user")
		}
		return
	}
	writeJSON(w, http.StatusCreated, userToPayload(u))
}

// UpdateUser — PATCH /api/v1/admin/users/{id} (admin only).
func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := mustBeAdmin(w, r); !ok {
		return
	}
	var body struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.Role != nil {
		if err := s.Deps.Users.SetRole(r.Context(), id, *body.Role); err != nil {
			respondUserMutationError(w, err)
			return
		}
	}
	if body.Disabled != nil {
		if err := s.Deps.Users.SetDisabled(r.Context(), id, *body.Disabled); err != nil {
			respondUserMutationError(w, err)
			return
		}
	}
	u, err := s.Deps.Users.GetByID(r.Context(), id)
	if err != nil {
		respondUserMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userToPayload(u))
}

// DeleteUser — DELETE /api/v1/admin/users/{id} (admin only).
func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok := mustBeAdmin(w, r); !ok {
		return
	}
	if err := s.Deps.Users.Delete(r.Context(), id); err != nil {
		respondUserMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func respondUserMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, users.ErrInvalidRole):
		writeError(w, http.StatusUnprocessableEntity, "role must be 'admin' or 'viewer'")
	case errors.Is(err, users.ErrLastAdminBlock):
		writeError(w, http.StatusForbidden, "cannot remove the last enabled admin")
	default:
		writeError(w, http.StatusInternalServerError, "user update failed")
	}
}

// ---------------------------------------------------------------------------
// API key endpoints
// ---------------------------------------------------------------------------

// ListApiKeys — GET /api/v1/api-keys.
func (s *Server) ListApiKeys(w http.ResponseWriter, r *http.Request, params openapi.ListApiKeysParams) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	wantAll := params.Owner != nil && *params.Owner == "all"
	if wantAll {
		if ac.User.Role != users.RoleAdmin {
			writeError(w, http.StatusForbidden, "owner=all is admin-only")
			return
		}
	}
	var (
		list []apikeys.ApiKey
		err  error
	)
	if wantAll {
		list, err = s.Deps.APIKeys.ListAll(r.Context())
	} else {
		list, err = s.Deps.APIKeys.ListForOwner(r.Context(), ac.User.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list api keys")
		return
	}
	out := make([]apiKeyPayload, 0, len(list))
	for _, k := range list {
		out = append(out, apiKeyToPayload(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_keys": out,
		"total":    len(out),
	})
}

// CreateApiKey — POST /api/v1/api-keys.
func (s *Server) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}
	full, ak, err := s.Deps.APIKeys.Generate(r.Context(), ac.User.ID, body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create api key")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"api_key":  apiKeyToPayload(ak),
		"full_key": full,
	})
}

// RevokeApiKey — DELETE /api/v1/api-keys/{id}.
func (s *Server) RevokeApiKey(w http.ResponseWriter, r *http.Request, id string) {
	ac, ok := authFromCtx(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	ak, err := s.Deps.APIKeys.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, apikeys.ErrNotFound) {
			writeError(w, http.StatusNotFound, "api key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not look up api key")
		return
	}
	if ak.OwnerUserID != ac.User.ID && ac.User.Role != users.RoleAdmin {
		// Hide existence from non-owners — same response as "not found".
		writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	if err := s.Deps.APIKeys.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, apikeys.ErrAlreadyRevoked) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke api key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Cookie helpers
// ---------------------------------------------------------------------------

// setSessionCookie writes the cix_session cookie. Secure flag is set
// when the request arrived via TLS — in dev (plain HTTP localhost) the
// flag is omitted so the browser actually stores it.
func setSessionCookie(w http.ResponseWriter, r *http.Request, id string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessions.CookieName,
		Value:    id,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessions.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

