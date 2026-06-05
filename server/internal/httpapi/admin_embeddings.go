// admin_embeddings.go implements the pluggable-embedding-provider
// admin surface:
//
//	GET  /api/v1/admin/embedding-providers          — registered kinds + schemas + env-key readiness
//	GET  /api/v1/admin/embedding-providers/active   — currently active kind + persisted config
//	PUT  /api/v1/admin/embedding-providers/active   — atomic switch (validate → persist → swap)
//	POST /api/v1/admin/embedding-providers/{kind}/test — pre-save sanity check using submitted config
//
// All routes are admin-only (mustBeAdmin). The handlers below are
// mounted directly onto the chi router in router.go — they are not
// part of the OpenAPI-generated handler set so the openapi.yaml /
// regenerated openapi.gen.go can be updated independently.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/dvcdsys/code-index/server/internal/embeddings"
	"github.com/dvcdsys/code-index/server/internal/embeddings/provider"
	"github.com/dvcdsys/code-index/server/internal/embeddingscfg"
	"github.com/dvcdsys/code-index/server/internal/httpapi/openapi"
)

// providerInfoPayload is the per-kind entry in GET /embedding-providers.
type providerInfoPayload struct {
	Kind       string             `json:"kind"`
	Schema     json.RawMessage    `json:"schema"`
	SecretEnvs []secretEnvPayload `json:"secret_envs"`
}

// secretEnvPayload tells the dashboard which env-var names a provider
// reads and whether they are currently set on the server. Used to
// render the "set CIX_VOYAGE_API_KEY before saving" banner.
type secretEnvPayload struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// activeProviderPayload is the GET /embedding-providers/active body.
// Config is the persisted JSON blob, returned verbatim — providers
// store env-key NAMES rather than values, so this is safe to render
// to admin clients.
type activeProviderPayload struct {
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
	// ID is Provider.ID() — surfaced so the UI can compare against
	// each project's indexed_with_model and render the stale-model
	// badge without going through /status.
	ID string `json:"id"`
}

// switchProviderRequest is the PUT /embedding-providers/active body.
type switchProviderRequest struct {
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
}

// testProviderResponse is what /test returns on success.
type testProviderResponse struct {
	OK        bool `json:"ok"`
	Dimension int  `json:"dimension,omitempty"`
}

// ListEmbeddingProviders — GET /api/v1/admin/embedding-providers.
func (s *Server) ListEmbeddingProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	kinds := provider.Kinds()
	out := make([]providerInfoPayload, 0, len(kinds))
	for _, kind := range kinds {
		f, ok := provider.Lookup(kind)
		if !ok {
			continue
		}
		envs := f.SecretEnvVars()
		envPayload := make([]secretEnvPayload, 0, len(envs))
		for _, name := range envs {
			_, present := os.LookupEnv(name)
			envPayload = append(envPayload, secretEnvPayload{Name: name, Set: present})
		}
		out = append(out, providerInfoPayload{
			Kind:       kind,
			Schema:     f.SchemaJSON(),
			SecretEnvs: envPayload,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// GetActiveEmbeddingProvider — GET /api/v1/admin/embedding-providers/active.
func (s *Server) GetActiveEmbeddingProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	embedSvc, ok := s.Deps.EmbeddingSvc.(*embeddings.Service)
	if !ok || embedSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "embeddings service not available")
		return
	}
	if s.Deps.EmbeddingsCfg == nil {
		writeError(w, http.StatusServiceUnavailable, "embedding provider store not available")
		return
	}
	snap, has, err := s.Deps.EmbeddingsCfg.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load active provider: "+err.Error())
		return
	}
	if !has {
		// No persisted row → fall back to the live provider's kind and
		// the env-derived config. This handles fresh installs where
		// the DB hasn't been seeded yet.
		snap = embeddingscfg.Snapshot{Kind: embedSvc.CurrentKind()}
	}
	writeJSON(w, http.StatusOK, activeProviderPayload{
		Kind:   snap.Kind,
		Config: snap.Config,
		ID:     embedSvc.EmbeddingModel(),
	})
}

// SwitchEmbeddingProvider — PUT /api/v1/admin/embedding-providers/active.
//
// Validate-then-persist: SwitchProvider builds + Start()s the new
// provider and swaps the live Service over (failing without side effects
// if the config is bad), and only on success is the new provider
// persisted. On any failure the previously-active provider stays the
// live AND persisted one.
func (s *Server) SwitchEmbeddingProvider(w http.ResponseWriter, r *http.Request) {
	user, ok := s.mustBeAdmin(w, r)
	if !ok {
		return
	}
	embedSvc, ok := s.Deps.EmbeddingSvc.(*embeddings.Service)
	if !ok || embedSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "embeddings service not available")
		return
	}
	if s.Deps.EmbeddingsCfg == nil {
		writeError(w, http.StatusServiceUnavailable, "embedding provider store not available")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req switchProviderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if req.Kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	if _, ok := provider.Lookup(req.Kind); !ok {
		writeError(w, http.StatusBadRequest, "unknown provider kind: "+req.Kind)
		return
	}

	// Special case for ollama: the per-kind form in the dashboard
	// does NOT carry ollama tuning fields (those live in the
	// runtime-config sections + env). When the admin switches back
	// to ollama from a remote provider, the dashboard sends an empty
	// blob; we synthesize the config here from the live runtime-cfg
	// snapshot applied to the env-derived defaults so the next
	// Start() has everything it needs (model, GGUF cache dir, llama
	// bin dir, transport, …).
	cfgBytes := req.Config
	if req.Kind == provider.KindOllama && (len(cfgBytes) == 0 || string(cfgBytes) == "{}" || string(cfgBytes) == "null") {
		envCfg := embedSvc.Config()
		if envCfg == nil {
			writeError(w, http.StatusInternalServerError, "ollama config: live cfg unavailable")
			return
		}
		// Merge the latest runtime-cfg overrides on top of env so a
		// recent PUT /admin/runtime-config (which doesn't auto-apply
		// while a remote provider is active) takes effect on switch.
		if s.Deps.RuntimeCfg != nil {
			snap, snapErr := s.Deps.RuntimeCfg.Get(r.Context())
			if snapErr == nil {
				snap.ApplyTo(envCfg)
			}
		}
		built, buildErr := embeddings.BuildOllamaConfigFromEnv(envCfg)
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "build ollama config: "+buildErr.Error())
			return
		}
		cfgBytes = built
	}
	if len(cfgBytes) == 0 {
		writeError(w, http.StatusBadRequest, "config is required")
		return
	}

	// Resolve the actor for the audit field. mustBeAdmin returns a nil
	// authContext when CIX_AUTH_DISABLED=true (a legitimate deployment
	// mode), so guard before dereferencing — matches the ac != nil
	// pattern used elsewhere (e.g. tunnels.go).
	actorID := ""
	if user != nil {
		actorID = user.User.ID
	}

	// Validate-then-persist: apply the swap live FIRST (SwitchProvider
	// builds + Start()s the new provider before touching live state, so a
	// bad config / unreachable endpoint / missing key fails here without
	// changing anything). Only persist once the swap succeeded. Persisting
	// first would let a transient switch failure leave the DB pointing at a
	// provider that won't start — which the boot path then re-adopts,
	// turning one failed switch into a delayed boot brick.
	if err := embedSvc.SwitchProvider(r.Context(), req.Kind, cfgBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "switch provider: "+err.Error())
		return
	}

	if err := s.Deps.EmbeddingsCfg.Save(r.Context(), embeddingscfg.Snapshot{
		Kind:   req.Kind,
		Config: cfgBytes,
	}, actorID); err != nil {
		// The live provider IS switched but we couldn't persist it. Report
		// the inconsistency loudly; a container restart will revert to the
		// previous persisted provider (safe — both old config and old
		// vectors are intact), so this degrades rather than corrupts.
		s.Deps.Logger.Error("embedding provider switched live but persist failed; will revert on restart",
			"kind", req.Kind, "err", err)
		writeError(w, http.StatusInternalServerError, "provider switched but persist failed (will revert on restart): "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, activeProviderPayload{
		Kind:   req.Kind,
		Config: cfgBytes,
		ID:     embedSvc.EmbeddingModel(),
	})
}

// TestEmbeddingProvider — POST /api/v1/admin/embedding-providers/{kind}/test.
//
// Builds a throw-away provider from the submitted config (without
// persisting it), Starts it, then Stops it. Returns the detected
// dimension and a success flag, or a typed error message the
// dashboard can render verbatim.
func (s *Server) TestEmbeddingProvider(w http.ResponseWriter, r *http.Request, kindParam openapi.TestEmbeddingProviderParamsKind) {
	if _, ok := s.mustBeAdmin(w, r); !ok {
		return
	}
	kind := string(kindParam)
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	if _, ok := provider.Lookup(kind); !ok {
		writeError(w, http.StatusBadRequest, "unknown provider kind: "+kind)
		return
	}
	// The /test endpoint Start()s a throw-away provider. For the HTTP-only
	// providers that is a harmless one-shot connect probe, but ollama's
	// Start() spawns a real llama-server child — on the SAME socket path as
	// the live sidecar, which would conflict with the running process. The
	// dashboard never tests ollama (it is configured via the runtime-config
	// form, not this endpoint), so reject it here rather than risk spawning
	// a competing sidecar from an ad-hoc admin call.
	if kind == provider.KindOllama {
		writeError(w, http.StatusBadRequest,
			"ollama is configured via the runtime-config form, not the provider test endpoint; testing it here would spawn a competing llama-server sidecar")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "config body is required")
		return
	}

	prov, err := provider.Build(r.Context(), kind, body, embeddings.EnvSecrets(), s.Deps.Logger)
	if err != nil {
		writeError(w, http.StatusBadRequest, "build provider: "+err.Error())
		return
	}
	if err := prov.Start(r.Context()); err != nil {
		// Distinguish missing-key from other failures — the dashboard
		// renders these differently (banner vs error toast).
		if errors.Is(err, provider.ErrMissingAPIKey) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	dim := prov.Dimension()
	_ = prov.Stop(r.Context())
	writeJSON(w, http.StatusOK, testProviderResponse{OK: true, Dimension: dim})
}
