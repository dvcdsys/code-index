package config

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

// validate is the singleton validator. Reused across calls so its tag cache
// is hot and so external code can register custom tags once.
var validate = validator.New(validator.WithRequiredStructEnabled())

// Validate runs struct-tag validation on cfg and returns a friendly,
// dotted-key error if any field fails. Returns nil when every constraint is
// satisfied. Slice elements are NOT validated transitively in this pass —
// add `dive` to the relevant slice field's tag if/when that's wanted.
//
// Validate is NOT called by Load() — a malformed value in an existing on-
// disk config should not brick the CLI. Validate is the gate for
// mutations: `cix config set`, the TUI's per-field error rendering, and
// any future `cix config doctor` will all call it.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	err := validate.Struct(cfg)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !asValidationErrors(err, &verrs) {
		return err
	}
	parts := make([]string, 0, len(verrs))
	for _, ve := range verrs {
		parts = append(parts, formatFieldError(ve))
	}
	return fmt.Errorf("config validation failed: %s", strings.Join(parts, "; "))
}

// asValidationErrors is a tiny helper that avoids importing errors.As at
// every call site and keeps the type assertion explicit.
func asValidationErrors(err error, out *validator.ValidationErrors) bool {
	if ve, ok := err.(validator.ValidationErrors); ok {
		*out = ve
		return true
	}
	return false
}

// formatFieldError turns a validator FieldError into a user-readable line.
// The validator reports paths in Go-struct form (e.g. Watcher.DebounceMS);
// callers usually want the dotted YAML/key form (e.g. watcher.debounce_ms).
// Translation is best-effort via a small hand-written map — keeping it
// hard-coded avoids paying for another reflect walk on every error.
func formatFieldError(ve validator.FieldError) string {
	key := goPathToKey(ve.Namespace())
	switch ve.Tag() {
	case "min":
		return fmt.Sprintf("%s must be ≥ %s (got %v)", key, ve.Param(), ve.Value())
	case "max":
		return fmt.Sprintf("%s must be ≤ %s (got %v)", key, ve.Param(), ve.Value())
	case "url":
		return fmt.Sprintf("%s must be a valid URL (got %q)", key, ve.Value())
	case "required":
		return fmt.Sprintf("%s is required", key)
	default:
		return fmt.Sprintf("%s failed %q (got %v)", key, ve.Tag(), ve.Value())
	}
}

// goPathToKey maps validator's Namespace ("Config.Watcher.DebounceMS") onto
// the user-facing dotted key ("watcher.debounce_ms"). Best-effort: unknown
// paths are lowercased.
var goPathToKey = func(ns string) string {
	switch ns {
	case "Config.Watcher.Enabled":
		return "watcher.enabled"
	case "Config.Watcher.DebounceMS":
		return "watcher.debounce_ms"
	case "Config.Watcher.ExcludePatterns":
		return "watcher.exclude"
	case "Config.Watcher.SyncIntervalMins":
		return "watcher.sync_interval_mins"
	case "Config.Indexing.BatchSize":
		return "indexing.batch_size"
	case "Config.Indexing.StreamingIdleTimeoutSec":
		return "indexing.streaming_idle_timeout_sec"
	case "Config.DefaultServer":
		return "default_server"
	}
	return strings.ToLower(ns)
}
