package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"

	"github.com/anthropics/code-index/cli/internal/config/schema"
)

// loadWithKoanf is the schema-driven config loader. It produces a *Config
// from these layers:
//
//  1. Defaults derived from `default:"…"` struct tags via schema.Walk.
//  2. The YAML file at ~/.cix/config.yaml (with legacy-key normalization
//     applied to the raw bytes pre-parse).
//
// Post-unmarshal, migrateToServers seeds the implicit localhost server and
// upgrades the legacy single-server `api:` block to the `servers:` list —
// same logic the original loader used.
//
// The needsResave flag tells the caller whether the on-disk file should be
// rewritten because the load process changed its representation
// (normalization rewrote keys, or a legacy `api:` block was migrated). The
// flag is never true when no file existed — the no-file case keeps the
// in-memory defaults but does not materialize them to disk.
func loadWithKoanf() (cfg *Config, needsResave bool, err error) {
	configDir, path, err := configPaths()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, false, fmt.Errorf("create config dir: %w", err)
	}

	k := koanf.New(".")

	// Layer 1: defaults from struct tags.
	if defaults := defaultsFromTags(); len(defaults) > 0 {
		if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
			return nil, false, fmt.Errorf("load defaults: %w", err)
		}
	}

	// Layer 2: YAML file (if present), with legacy-key normalization.
	var (
		fileExists         bool
		changedByNormalize bool
	)
	data, ferr := os.ReadFile(path)
	if ferr != nil && !os.IsNotExist(ferr) {
		return nil, false, fmt.Errorf("read config: %w", ferr)
	}
	if ferr == nil {
		fileExists = true
		normalized := normalizeLegacyKeys(data)
		changedByNormalize = !bytes.Equal(data, normalized)
		if err := k.Load(rawbytes.Provider(normalized), yaml.Parser()); err != nil {
			return nil, false, fmt.Errorf("parse config: %w", err)
		}
	}

	var out Config
	if err := k.UnmarshalWithConf("", &out, koanf.UnmarshalConf{Tag: "yaml"}); err != nil {
		return nil, false, fmt.Errorf("unmarshal config: %w", err)
	}

	migrated := migrateToServers(&out)
	needsResave = fileExists && (changedByNormalize || migrated)
	return &out, needsResave, nil
}

// configPaths returns the config directory and the config file path for the
// current user. Extracted so both loaders share the resolution.
func configPaths() (dir, path string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("get home dir: %w", err)
	}
	dir = filepath.Join(home, ".cix")
	path = filepath.Join(dir, "config.yaml")
	return dir, path, nil
}

// defaultsFromTags walks the Config schema and returns a flat
// dotted-key → typed-value map suitable for koanf's confmap provider.
//
// Fields without a `default:` tag are omitted (they get Go's zero value
// after unmarshal, which is the same behavior as the legacy defaults()
// function for any field it didn't seed). Slices are parsed as
// comma-separated lists with whitespace trimmed.
func defaultsFromTags() map[string]any {
	out := map[string]any{}
	_ = schema.Walk(&Config{}, func(l schema.LeafField) {
		raw := l.Tag("default")
		if raw == "" {
			return
		}
		val, ok := parseDefaultValue(raw, l.Field.Type)
		if !ok {
			return
		}
		out[l.Path] = val
	})
	return out
}

// parseDefaultValue converts a `default:"…"` tag string into a typed Go
// value matching the field's reflect.Type. Returns ok=false for unsupported
// kinds (e.g. nested structs), in which case the field falls back to its
// zero value after unmarshal.
func parseDefaultValue(raw string, t reflect.Type) (any, bool) {
	switch t.Kind() {
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, false
		}
		return v, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		return int(v), true
	case reflect.String:
		return raw, true
	case reflect.Slice:
		if t.Elem().Kind() != reflect.String {
			return nil, false
		}
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out, true
	}
	return nil, false
}
