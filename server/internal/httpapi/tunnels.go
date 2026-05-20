package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
	"github.com/dvcdsys/code-index/server/internal/tunnelcfg"
	"github.com/dvcdsys/code-index/server/internal/tunnels"
)

// disabledTunnelStatus is the snapshot returned when no tunnel is wired
// (CIX_TUNNEL_ENABLED=false). The cloudflare slot is "available but off";
// ngrok is reported as not-yet-implemented.
func disabledTunnelStatus() tunnels.Status {
	return tunnels.Status{Provider: tunnels.ProviderCloudflare, State: tunnels.StateDisabled, Available: true}
}

func disabledCatalog() []tunnels.Status {
	return []tunnels.Status{
		{Provider: tunnels.ProviderCloudflare, State: tunnels.StateDisabled, Available: true},
		{Provider: tunnels.ProviderNgrok, State: tunnels.StateDisabled, Available: true},
	}
}

// GetTunnelConfig — GET /api/v1/tunnels/config. Admin-only. The connector
// token is never returned (token_set reports presence).
func (s *Server) GetTunnelConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.Deps.TunnelConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "tunnel config is not configured on this server")
		return
	}
	cfg, err := s.Deps.TunnelConfig.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load tunnel config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// UpdateTunnelConfig — PUT /api/v1/tunnels/config. Admin-only. Persists the
// config then applies it (enable→start, disable→stop, change→restart).
func (s *Server) UpdateTunnelConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	if s.Deps.TunnelConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "tunnel config is not configured on this server")
		return
	}
	var body openapi.TunnelConfigUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	patch := tunnelcfg.Patch{
		Enabled:  body.Enabled,
		Hostname: body.Hostname,
		Token:    body.Token,
	}
	if body.Provider != nil {
		pv := string(*body.Provider)
		patch.Provider = &pv
	}
	if body.Mode != nil {
		m := string(*body.Mode)
		patch.Mode = &m
	}
	updatedBy := ""
	if ac != nil {
		updatedBy = ac.User.Email
	}
	if _, err := s.Deps.TunnelConfig.Set(r.Context(), patch, updatedBy); err != nil {
		// Set errors are configuration-validation failures (e.g. named mode
		// without a hostname/token) — surface as 400.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if s.Deps.Tunnel == nil {
		writeJSON(w, http.StatusOK, disabledTunnelStatus())
		return
	}
	resolved, err := s.Deps.TunnelConfig.Resolve(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve tunnel config: "+err.Error())
		return
	}
	// Apply on a background-derived deadline so a client disconnect doesn't
	// abort the tunnel mid-start; bounded so the request still returns.
	applyCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if aerr := s.Deps.Tunnel.Apply(applyCtx, tunnels.Settings{
		Enabled:  resolved.Enabled,
		Provider: resolved.Provider,
		Mode:     resolved.Mode,
		Hostname: resolved.Hostname,
		Token:    resolved.Token,
	}); aerr != nil {
		// Applied + persisted, but the tunnel failed to start. Report the
		// resulting (failed) status rather than a hard error so the UI can
		// show the reason inline.
		s.Deps.Logger.Warn("tunnel apply failed", "err", aerr)
	}
	writeJSON(w, http.StatusOK, s.Deps.Tunnel.Status())
}

// ListTunnelBinaries — GET /api/v1/tunnels/binaries. Per-provider agent
// binary status (installed/path/version/managed). Admin-only.
func (s *Server) ListTunnelBinaries(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if s.Deps.Tunnel == nil {
		writeJSON(w, http.StatusOK, map[string]any{"binaries": []tunnels.BinaryStatus{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"binaries": s.Deps.Tunnel.Binaries(r.Context())})
}

// InstallTunnelBinary — POST /api/v1/tunnels/binaries/{provider}/install.
func (s *Server) InstallTunnelBinary(w http.ResponseWriter, r *http.Request, provider openapi.InstallTunnelBinaryParamsProvider) {
	s.installTunnelBinary(w, r, string(provider))
}

// UpdateTunnelBinary — POST /api/v1/tunnels/binaries/{provider}/update.
// Same operation as install (fetch latest); separate route for clarity.
func (s *Server) UpdateTunnelBinary(w http.ResponseWriter, r *http.Request, provider openapi.UpdateTunnelBinaryParamsProvider) {
	s.installTunnelBinary(w, r, string(provider))
}

func (s *Server) installTunnelBinary(w http.ResponseWriter, r *http.Request, provider string) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	if provider != tunnels.ProviderCloudflare && provider != tunnels.ProviderNgrok {
		writeError(w, http.StatusBadRequest, "unknown provider: "+provider)
		return
	}
	if s.Deps.Tunnel == nil {
		writeError(w, http.StatusServiceUnavailable, "tunnel manager is not configured on this server")
		return
	}
	if err := s.Deps.Tunnel.InstallBinary(r.Context(), provider); err != nil {
		if errors.Is(err, tunnels.ErrBinManagementDisabled) {
			writeError(w, http.StatusConflict, "binary management is disabled (set CIX_TUNNEL_BIN_MANAGED=true)")
			return
		}
		writeError(w, http.StatusInternalServerError, "install failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"binaries": s.Deps.Tunnel.Binaries(r.Context())})
}

// ListTunnels — GET /api/v1/tunnels. Provider catalog + active status.
func (s *Server) ListTunnels(w http.ResponseWriter, _ *http.Request) {
	cat := disabledCatalog()
	if s.Deps.Tunnel != nil {
		cat = s.Deps.Tunnel.Catalog()
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": cat})
}

// GetTunnelStatus — GET /api/v1/tunnels/status. Active provider snapshot.
func (s *Server) GetTunnelStatus(w http.ResponseWriter, _ *http.Request) {
	st := disabledTunnelStatus()
	if s.Deps.Tunnel != nil {
		st = s.Deps.Tunnel.Status()
	}
	writeJSON(w, http.StatusOK, st)
}

// TestTunnel — POST /api/v1/tunnels/test. End-to-end round-trip probe.
func (s *Server) TestTunnel(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Tunnel == nil {
		writeError(w, http.StatusConflict, "no active tunnel (CIX_TUNNEL_ENABLED=false)")
		return
	}
	res, err := s.Deps.Tunnel.TestConnection(r.Context())
	if err != nil {
		if errors.Is(err, tunnels.ErrNoActiveTunnel) {
			writeError(w, http.StatusConflict, "no active tunnel")
			return
		}
		writeError(w, http.StatusInternalServerError, "tunnel test failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// RestartTunnel — POST /api/v1/tunnels/restart. Restart the subprocess.
func (s *Server) RestartTunnel(w http.ResponseWriter, r *http.Request) {
	if s.Deps.Tunnel == nil {
		writeError(w, http.StatusConflict, "no active tunnel (CIX_TUNNEL_ENABLED=false)")
		return
	}
	if err := s.Deps.Tunnel.Restart(r.Context()); err != nil {
		if errors.Is(err, tunnels.ErrNoActiveTunnel) {
			writeError(w, http.StatusConflict, "no active tunnel")
			return
		}
		writeError(w, http.StatusInternalServerError, "tunnel restart failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Deps.Tunnel.Status())
}

// ReconcileWebhooks — POST /api/v1/github/webhooks/reconcile. Re-register
// every webhook_mode=auto repo against the current public base URL.
func (s *Server) ReconcileWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.Deps.WebhookReconciler == nil || s.Deps.GitRepos == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub repo support is not configured on this server")
		return
	}
	res, err := s.Deps.WebhookReconciler.Reconcile(r.Context(), s.publicBaseURL())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reconcile failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
