package provider

import (
	"fmt"
	"strings"
)

// This file holds the plumbing every HTTP-only embedding provider
// (openai, voyage, and any future REST backend) shares: base-URL
// normalization, live API-key resolution, and the Ready()/Status()
// implementations. Keeping it in one place stops the providers from
// drifting apart (they previously copy-pasted all four).

// NormalizeBaseURL trims trailing slashes from an HTTP base URL so that
// joining "/v1/embeddings" never yields a double slash — which stricter
// OpenAI-compatible servers behind a proxy (vLLM/TEI) 404 on.
func NormalizeBaseURL(raw string) string {
	return strings.TrimRight(raw, "/")
}

// ResolveAPIKey looks up apiKeyEnv via secrets, returning (value, true)
// only when the env var is set to a non-empty value. HTTP providers read
// their bearer token live through this — the raw value never lives in the
// provider's persisted config. Returns ("", false) when secrets is nil,
// apiKeyEnv is empty, or the var is unset/blank.
func ResolveAPIKey(secrets SecretLookup, apiKeyEnv string) (string, bool) {
	if secrets == nil || apiKeyEnv == "" {
		return "", false
	}
	v, ok := secrets(apiKeyEnv)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// RemoteReady is the shared Ready() for HTTP-only providers: nil when the
// API key is present, ErrMissingAPIKey otherwise. We deliberately do NOT
// ping the upstream on every call (the /status footer polls Ready every
// 30s) — real outages surface as embed failures with diagnostics.
func RemoteReady(secrets SecretLookup, apiKeyEnv string) error {
	if _, ok := ResolveAPIKey(secrets, apiKeyEnv); !ok {
		return fmt.Errorf("%w: %s", ErrMissingAPIKey, apiKeyEnv)
	}
	return nil
}

// RemoteStatus is the shared Status() for HTTP-only providers: StateRemote
// when the API key is present, StateFailed with a diagnostic otherwise.
func RemoteStatus(model, apiKeyEnv string, secrets SecretLookup) Status {
	st := Status{
		State:          StateRemote,
		ManagesProcess: false,
		Model:          model,
	}
	if _, ok := ResolveAPIKey(secrets, apiKeyEnv); !ok {
		st.State = StateFailed
		st.LastError = "API key env var " + apiKeyEnv + " is not set"
	}
	return st
}
