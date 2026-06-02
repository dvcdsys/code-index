// Package provider defines the pluggable embedding-backend abstraction
// used by embeddings.Service. Implementations live in sub-packages
// (provider/ollama, provider/openai, provider/voyage). The Service holds
// exactly one active Provider; switching provider at runtime is what the
// admin /api/v1/admin/embedding-providers/active endpoint does.
//
// Identity & reindex. Every Provider exposes ID() — a stable fingerprint
// string written to projects.indexed_with_model at index time. When the
// active provider's ID() changes (different kind, model, or dimension),
// every project's stored fingerprint is stale; the existing per-project
// drift check in internal/repojobs detects it on the next clone job and
// forces a full reindex. Provider switching therefore reuses the model-
// change pipeline unchanged.
//
// Lifecycle. Start is called once after construction (ollama spawns the
// child process; HTTP-only providers do a connect-test). Stop is called
// before switching to a different provider and on server shutdown.
package provider

import (
	"context"
	"errors"
	"strings"
)

// Kind enumerates the built-in provider kinds. New kinds are added by
// registering a Factory with that kind string.
const (
	KindOllama = "ollama"
	KindOpenAI = "openai"
	KindVoyage = "voyage"
)

// Provider is the embedding backend abstraction. Implementations cover
// one upstream service each (the in-process llama-server sidecar, the
// OpenAI-compatible /v1/embeddings REST API, the Voyage AI REST API, …).
//
// Concurrency. All methods on a Provider must be safe for concurrent
// use; Service brackets them with a Queue (rate-limit / backpressure)
// but does not serialise them.
//
// Errors. Implementations should wrap upstream HTTP / process failures
// with enough context for an operator to diagnose. Use ErrNotReady when
// the backend is alive but not yet able to serve (e.g. ollama warming
// up); the Service layer treats it as a retriable busy signal.
type Provider interface {
	// Kind returns the registered factory kind for this provider, e.g.
	// "ollama", "openai", "voyage".
	Kind() string

	// ID returns the fingerprint that uniquely identifies this provider
	// configuration for the purposes of index invalidation. Format:
	// "{kind}:{model}[:{dim}][:{dtype}]". The string is opaque to
	// callers — they only compare it for equality.
	ID() string

	// Dimension reports the embedding vector dimension this provider
	// will produce. Used by the vector store to dimension the Chroma
	// collection when it is first created. May be 0 if the dimension
	// is only known after the first embed call; the vectorstore then
	// infers it from the first upsert as before.
	Dimension() int

	// SupportsTokenize reports whether the provider implements
	// TokenizeAndEmbed natively. The Service uses this to decide
	// whether to call TokenizeAndEmbed or fall back to EmbedDocuments
	// in the indexer's chunking path.
	SupportsTokenize() bool

	// Start prepares the provider for serving requests. For ollama
	// this spawns the llama-server child process and blocks until the
	// readiness probe succeeds. For HTTP-only providers it performs
	// a one-shot connect-test against the configured endpoint with
	// any provided API key. ctx bounds the startup; on failure the
	// caller may try a different config without calling Stop first.
	Start(ctx context.Context) error

	// Stop tears the provider down within the ctx deadline. Idempotent
	// and safe to call on a provider that never Start()-ed.
	Stop(ctx context.Context) error

	// Ready reports whether the provider is currently able to serve an
	// embedding request. nil = ready. Returning ErrNotReady is the
	// recommended busy signal during startup / restart windows.
	Ready(ctx context.Context) error

	// Status returns a snapshot for the dashboard. Fields that are
	// not meaningful for a given provider (e.g. PID for HTTP-only
	// providers) should be zero-valued.
	Status() Status

	// EmbedQuery embeds a single query string. Providers that support
	// asymmetric retrieval apply their model-specific transform here
	// (ollama prepends the model's query prefix; voyage sends
	// input_type=query; openai applies nothing).
	EmbedQuery(ctx context.Context, query string) ([]float32, error)

	// EmbedDocuments embeds a batch of passages / chunks. Returned
	// vectors follow input order. Empty input is a no-op returning
	// (nil, nil).
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)

	// TokenizeAndEmbed is the token-aware embedding pipeline used by
	// the indexer for chunks that may exceed the model's context
	// window. Providers without native tokenization (SupportsTokenize
	// returns false) may implement this as a pass-through to
	// EmbedDocuments — callers must use SupportsTokenize() to decide
	// whether to chunk inputs themselves.
	TokenizeAndEmbed(ctx context.Context, texts []string) ([][]float32, error)

	// StorageComponents returns the on-disk path components that
	// namespace this provider's vector store, MOST-significant first:
	// {kind, model-slug[, variant]}. Each component is already
	// filesystem-safe (run through StorageSlug at the source). The
	// vector-store dir is filepath.Join(ChromaPersistDir, components...).
	//
	// Crucially these are STRUCTURED fields the provider knows directly
	// — never derived by re-parsing the flattened ID() — so the kind
	// (always its own path segment) can never collide with a model name
	// that happens to normalise to "ollama_…"/"voyage_…". That collision
	// is what the flat single-slug scheme suffered from.
	StorageComponents() []string
}

// State enumerates the dashboard-facing provider states surfaced via
// Status. Implementations should pick the closest match.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateFailed   = "failed"
	StateDisabled = "disabled"
	// StateRemote marks an HTTP-only provider that has no managed
	// process: it cannot fail to "start" beyond a config-time connect
	// test, and uptime / pid / restart concepts do not apply. Footer
	// indicator stays permanently green for this state.
	StateRemote = "remote"
)

// Status is the dashboard-facing snapshot of a provider's runtime
// state. Sidecar-specific fields (PID, Uptime, restart counts) are
// zero / empty for HTTP-only providers.
type Status struct {
	// State is one of the State* constants above.
	State string `json:"state"`

	// ManagesProcess reports whether this provider manages an
	// in-process child (true for ollama, false for HTTP providers).
	// The footer uses this to decide whether to render an
	// alive/red-dot indicator or a permanent green dot.
	ManagesProcess bool `json:"manages_process"`

	// Model is the human-readable model identifier (HF repo id,
	// OpenAI model name, Voyage model name).
	Model string `json:"model"`

	// PID is the child process id when ManagesProcess. Zero otherwise.
	PID int `json:"pid,omitempty"`

	// UptimeSeconds is the time the current child has been alive.
	// Zero for remote providers.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`

	// LastError surfaces the most recent spawn / health-probe / HTTP
	// error so the dashboard can render it without grepping logs.
	// Empty when healthy.
	LastError string `json:"last_error,omitempty"`

	// InFlight reports queue depth at the Service layer. The Service
	// fills this in after the provider returns its Status; providers
	// should leave it at 0.
	InFlight int `json:"in_flight"`
}

// ErrNotReady signals that the provider is alive but not yet able to
// serve a request (e.g. ollama warming up after Start). The Service
// layer translates this to a busy-style 503 with a Retry-After hint.
var ErrNotReady = errors.New("provider: not ready")

// ErrMissingAPIKey signals that the provider was constructed against
// a config naming an env-var that is not set at the moment of the
// call. The admin /test endpoint reports it verbatim so the dashboard
// can guide the operator to set the env var before saving.
var ErrMissingAPIKey = errors.New("provider: required API key env var is not set")

// ErrUnrecoverable signals a terminal provider failure — e.g. the
// ollama sidecar exceeded its crash-restart budget. Subsequent calls
// return this until the provider is replaced (admin perspective) or
// the process is restarted. Caller maps to HTTP 503 without retry.
var ErrUnrecoverable = errors.New("provider: unrecoverable failure")

// StorageSlug turns a Provider.ID() fingerprint into a filesystem-safe
// slug used to namespace the on-disk vector store directory, so each
// distinct embedding identity (kind + model + dim + dtype) gets its own
// chroma collection space. Switching providers therefore never mixes
// vectors of different dimensions in one collection, and switching back
// reuses the prior namespace without a reindex.
//
// Rules: lowercase, then replace every rune outside [a-z0-9_] (including
// '/', '-', ':') with '_'. Deliberately a pure per-rune map — no
// run-collapsing or trimming — so the transform is deterministic and
// idempotent. (It is not strictly injective: e.g. "a:b" and "a-b" both
// map to "a_b". That is harmless here because real Provider.ID() strings
// for a given kind never differ only in a separator — model names carry
// no ':' and dims/dtypes are fixed tokens.) Examples:
//
//	"voyage:voyage-code-3:2048:float"            → "voyage_voyage_code_3_2048_float"
//	"ollama:awhiteside/CodeRankEmbed-Q8_0-GGUF"  → "ollama_awhiteside_coderankembed_q8_0_gguf"
//	"openai:text-embedding-3-large:256"          → "openai_text_embedding_3_large_256"
//
// An empty ID yields an empty slug; callers guard against that.
func StorageSlug(id string) string {
	lower := strings.ToLower(id)
	var b strings.Builder
	b.Grow(len(lower))
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SecretLookup resolves an env-var name to its current value at the
// moment of the call. Implementations must return (value, true) when
// the env var is set (even if empty), and ("", false) when it is
// unset. This is the only surface providers see for secrets; the
// raw value never lives in the Provider's config struct.
type SecretLookup func(envVarName string) (value string, ok bool)
