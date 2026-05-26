package provider

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Factory constructs Providers of a given kind from a typed Config.
// One Factory per kind; registered at init time by each provider
// sub-package via Register. The Service builds the active provider
// by calling Build on the factory matching the persisted kind.
type Factory interface {
	// Kind matches one of the Kind* constants in provider.go.
	Kind() string

	// SchemaJSON returns a JSON-encoded ConfigSchema describing the
	// fields this provider accepts. The dashboard reads this to know
	// which input controls to render in the provider-specific form.
	// Returned bytes must be deterministic across calls so the API
	// response is cacheable.
	SchemaJSON() []byte

	// SecretEnvVars lists the env-var names this provider consults
	// for credentials. Order is irrelevant. Used by the admin
	// GET /embedding-providers endpoint to tell the dashboard which
	// keys are set in the environment so it can render a "set
	// CIX_VOYAGE_API_KEY before saving" banner when the operator
	// configures a provider whose key is missing.
	SecretEnvVars() []string

	// Build constructs a concrete Provider from a JSON-encoded config
	// blob (shape matches the provider's ConfigSchema). secrets is
	// used by HTTP-only providers to resolve API-key env-var names;
	// ollama's factory ignores it. logger may be nil; implementations
	// fall back to slog.Default().
	//
	// Build does NOT start the provider — call Provider.Start
	// separately so the caller can decide whether a failed Start
	// rolls back to the previous active provider.
	Build(cfg []byte, secrets SecretLookup, logger *slog.Logger) (Provider, error)
}

// ConfigSchema is the dashboard-facing description of a provider's
// config form. Hardcoded React components in the dashboard ignore
// this in favour of typed forms, but the JSON is still exposed via
// /admin/embedding-providers so external tooling (curl, ad-hoc
// admin scripts) has a contract to read.
type ConfigSchema struct {
	Fields []ConfigField `json:"fields"`
}

// ConfigField describes one input control in a provider config form.
// Kind drives the widget; the dashboard's hardcoded forms map field
// Name → input.
type ConfigField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"` // "string" | "int" | "bool" | "enum" | "secret-env"
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a Factory to the global registry. Called from each
// provider sub-package's init(). Panics if the kind is already
// registered — that indicates a programmer error (duplicate
// init) rather than something an operator can recover from.
func Register(f Factory) {
	if f == nil {
		panic("provider.Register: nil factory")
	}
	kind := f.Kind()
	if kind == "" {
		panic("provider.Register: factory returned empty Kind")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[kind]; exists {
		panic(fmt.Sprintf("provider.Register: kind %q already registered", kind))
	}
	registry[kind] = f
}

// Lookup returns the Factory registered for kind, or (nil, false).
func Lookup(kind string) (Factory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[kind]
	return f, ok
}

// Kinds returns the registered kinds in deterministic order, useful
// for the admin /embedding-providers list endpoint.
func Kinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	kinds := make([]string, 0, len(registry))
	for k := range registry {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Build constructs a provider by kind. Convenience wrapper around
// Lookup + Factory.Build that returns a clear error when the kind
// is not registered (so callers don't have to fmt.Errorf at each
// site).
func Build(ctx context.Context, kind string, cfg []byte, secrets SecretLookup, logger *slog.Logger) (Provider, error) {
	_ = ctx // reserved for future per-build cancellation; Build itself is fast
	f, ok := Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", kind)
	}
	return f.Build(cfg, secrets, logger)
}

// EnvSecrets returns a SecretLookup that reads from os.LookupEnv.
// This is the production wiring; tests pass their own SecretLookup
// returning fixed values so the test does not have to set env vars
// in the process.
func EnvSecrets(lookup func(key string) (string, bool)) SecretLookup {
	return SecretLookup(lookup)
}
