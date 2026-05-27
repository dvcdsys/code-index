package voyage

// ModelLimits captures the documented per-model rate-limit ceiling
// for one model at Tier 1 (the baseline tier). Voyage does NOT expose
// these values via API — they live only in the public docs at
// https://docs.voyageai.com/docs/rate-limits and in the web dashboard
// under Settings → Rate Limits.
//
// Tier 2 (>= $100 paid lifetime) and Tier 3 (>= $1000) multiply the
// RPM / TPM ceilings by 2x and 3x respectively; per-request limits
// (inputs / tokens) are not tier-dependent.
//
// We surface these numbers in the dashboard so an operator can sanity
// check the configured concurrency against their plan without bouncing
// to a separate tab. They are NOT enforced client-side — Voyage rejects
// over-limit traffic with 429 / 400 which the server already surfaces.
type ModelLimits struct {
	// Model is the API model name (matches Config.Model).
	Model string `json:"model"`

	// Tier1RPM / Tier1TPM are the per-minute caps at the baseline
	// (free-but-billable) tier. Multiply by 2 for Tier 2, 3 for Tier 3.
	Tier1RPM int `json:"tier1_rpm"`
	Tier1TPM int `json:"tier1_tpm"`

	// MaxInputsPerRequest is the hard cap on `input` array length per
	// /v1/embeddings POST. voyage-code-* models cap at 128; voyage-3*
	// models accept up to 1000.
	MaxInputsPerRequest int `json:"max_inputs_per_request"`

	// MaxTokensPerRequest is the hard cap on total tokens in a single
	// /v1/embeddings POST after Voyage's own tokenizer pass. 120000
	// for all current text models; we keep it per-model in case Voyage
	// differentiates later.
	MaxTokensPerRequest int `json:"max_tokens_per_request"`

	// Notes is free-form text the dashboard renders verbatim. Used to
	// flag oddities (e.g. "TPM doubled in v3.5", "free tier overrides
	// these values to 3/10K").
	Notes string `json:"notes,omitempty"`
}

// KnownLimitsSource describes where the values below came from. The
// dashboard surfaces this string so an operator can tell when the
// snapshot was taken (and whether they should consult the docs for a
// newer version).
const KnownLimitsSource = "Voyage public docs at https://docs.voyageai.com/docs/rate-limits (snapshot 2026-05-27). Free tier is capped at 3 RPM / 10K TPM regardless of model."

// KnownModelLimits is the hardcoded snapshot of documented Voyage
// rate limits. Keys match the model strings the dashboard offers in
// the provider dropdown. Add new models here when Voyage publishes
// them and bump KnownLimitsSource's snapshot date.
var KnownModelLimits = map[string]ModelLimits{
	"voyage-code-3": {
		Model: "voyage-code-3", Tier1RPM: 2000, Tier1TPM: 3_000_000,
		MaxInputsPerRequest: 128, MaxTokensPerRequest: 120_000,
		Notes: "Optimized for code retrieval.",
	},
	"voyage-code-2": {
		Model: "voyage-code-2", Tier1RPM: 2000, Tier1TPM: 3_000_000,
		MaxInputsPerRequest: 128, MaxTokensPerRequest: 120_000,
		Notes: "Predecessor of voyage-code-3.",
	},
	"voyage-3-large": {
		Model: "voyage-3-large", Tier1RPM: 2000, Tier1TPM: 3_000_000,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 120_000,
		Notes: "General-purpose multilingual.",
	},
	"voyage-3": {
		Model: "voyage-3", Tier1RPM: 2000, Tier1TPM: 3_000_000,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 120_000,
	},
	"voyage-3-lite": {
		Model: "voyage-3-lite", Tier1RPM: 2000, Tier1TPM: 3_000_000,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 120_000,
		Notes: "Smaller, cheaper than voyage-3.",
	},
	"voyage-3.5": {
		Model: "voyage-3.5", Tier1RPM: 2000, Tier1TPM: 8_000_000,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 120_000,
		Notes: "Higher TPM than voyage-3.",
	},
	"voyage-3.5-lite": {
		Model: "voyage-3.5-lite", Tier1RPM: 2000, Tier1TPM: 16_000_000,
		MaxInputsPerRequest: 1000, MaxTokensPerRequest: 120_000,
		Notes: "Highest TPM among lite models.",
	},
}

// LimitsList returns the known limits as a slice, ordered by model
// name for deterministic JSON. Convenience for the admin endpoint.
func LimitsList() []ModelLimits {
	out := make([]ModelLimits, 0, len(KnownModelLimits))
	// Preserve dashboard order (matches the factory's model enum).
	order := []string{"voyage-code-3", "voyage-3-large", "voyage-3", "voyage-3-lite", "voyage-code-2", "voyage-3.5", "voyage-3.5-lite"}
	seen := map[string]bool{}
	for _, m := range order {
		if l, ok := KnownModelLimits[m]; ok {
			out = append(out, l)
			seen[m] = true
		}
	}
	// Any extras not in the canonical order go to the tail.
	for k, v := range KnownModelLimits {
		if !seen[k] {
			out = append(out, v)
		}
	}
	return out
}
