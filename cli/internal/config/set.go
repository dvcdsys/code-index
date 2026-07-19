package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/dvcdsys/code-index/cli/internal/config/schema"
)

// ErrUnknownKey is returned by SetByPath when key does not match any
// schema-tagged leaf in the Config struct. Callers (notably runConfigSet)
// use errors.Is(err, ErrUnknownKey) to decide whether to fall through to a
// legacy handler.
var ErrUnknownKey = errors.New("unknown config key")

// SetByPath looks up the schema leaf identified by key, parses value per
// the leaf's Go type, applies it, runs full-struct validation, and persists.
//
// Parsing rules:
//   - bool      strconv.ParseBool          ("true"/"false"/"1"/"0"/etc.)
//   - int*      strconv.ParseInt(base=10)
//   - string    used verbatim
//   - []string  comma-separated, each entry trimmed; REPLACE semantics
//     (the new value REPLACES the existing slice — there is no append form
//     on `config set`)
//
// Server-management keys (`server.<name>.url|key`, `default_server` aliases
// for legacy `api.*`) are NOT handled here — they live in runConfigSet's
// dedicated branch because they have side effects (upsert into Servers,
// reassign DefaultServer) that don't fit the "parse-and-assign" model.
//
// Slices of structs (Servers, Projects) are deliberately rejected: there's
// no sensible string serialization for them and they have purpose-built
// CRUD helpers (SetServerURL, AddProject, …).
func SetByPath(key, value string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	var (
		found    bool
		applyErr error
		// On validation failure we restore the field to its prior value so
		// the in-memory singleton stays consistent with the on-disk file
		// (which was NOT written). Save the prior snapshot as a detached
		// reflect.Value so a re-assignment via leaf.Value.Set() can undo
		// the mutation without re-walking the schema.
		leafRef  schema.LeafField
		priorVal reflect.Value
	)
	walkErr := schema.Walk(cfg, func(l schema.LeafField) {
		if found || l.Path != key {
			return
		}
		found = true
		leafRef = l
		priorVal = reflect.New(l.Value.Type()).Elem()
		priorVal.Set(l.Value)
		applyErr = applyLeafValue(l, value)
	})
	if walkErr != nil {
		return walkErr
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	if applyErr != nil {
		// Best-effort restore even on parse failure (applyLeafValue may
		// have partially mutated for some kinds in the future).
		leafRef.Value.Set(priorVal)
		return applyErr
	}

	if err := Validate(cfg); err != nil {
		leafRef.Value.Set(priorVal)
		return err
	}
	return Save(cfg)
}

func applyLeafValue(l schema.LeafField, raw string) error {
	if !l.Value.CanSet() {
		return fmt.Errorf("config key %q cannot be set", l.Path)
	}
	v := l.Value
	switch v.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid bool %q (use true/false)", l.Path, raw)
		}
		v.SetBool(b)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q", l.Path, raw)
		}
		if v.OverflowInt(n) {
			return fmt.Errorf("%s: value %d out of range", l.Path, n)
		}
		v.SetInt(n)
		return nil

	case reflect.String:
		v.SetString(raw)
		return nil

	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("%s: list keys with non-string elements are not settable via 'config set'", l.Path)
		}
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		v.Set(reflect.ValueOf(out))
		return nil
	}
	return fmt.Errorf("%s: unsupported field kind %s", l.Path, v.Kind())
}
