// Package schema provides a tag-driven walker over the Config struct.
//
// The walker is the single source of truth for "what fields exist, where do
// they live, what are their defaults/descriptions/validators". Every config
// surface that needs that knowledge (show, set, keys, edit, init, defaults
// seeding) calls Walk and acts on the LeafField it yields, instead of
// hard-coding a switch.
package schema

import (
	"fmt"
	"reflect"
)

// LeafField is one annotated leaf in the Config struct tree.
//
// Path is the dotted key from the `key:` tag (e.g. "watcher.debounce_ms").
// Field carries the original reflect.StructField so callers can read all
// other tags (desc, default, env, validate, sensitive) without re-parsing.
// Value is the live reflect.Value pointing at the field — callers can
// read (Render) or mutate it (Set) via the standard reflect API.
type LeafField struct {
	Path  string
	Field reflect.StructField
	Value reflect.Value
}

// Tag returns the value of a struct tag on this leaf's field.
// Convenience wrapper so callers don't need to import reflect.
func (l LeafField) Tag(name string) string {
	return l.Field.Tag.Get(name)
}

// Sensitive reports whether this leaf is marked `sensitive:"true"`.
// CodeQL sensitive-name heuristics flag any read of a *Key/*Secret value
// into a named variable, so renderers MUST use this gate instead of
// inspecting the value itself.
func (l LeafField) Sensitive() bool {
	return l.Tag("sensitive") == "true"
}

// LeafVisitor is invoked once per annotated leaf in Walk order
// (structural order of the source struct).
type LeafVisitor func(leaf LeafField)

// Walk traverses cfg (a struct or pointer to a struct) and invokes visit for
// every field that has a `key:` struct tag.
//
// Rules:
//   - A field WITH a `key:` tag is yielded as a leaf regardless of its Go
//     kind. Slice fields (Servers, Projects) are yielded as a single leaf;
//     callers render them with their own formatter.
//   - A field WITHOUT a `key:` tag that is itself a struct is recursed into.
//     Containers like `Watcher`, `Server`, `Indexing` carry no key tag
//     themselves — their child fields each carry the full dotted key
//     (`watcher.debounce_ms`, …).
//   - A field WITHOUT a `key:` tag that is a scalar/slice is skipped
//     entirely. This is how legacy fields (the auto-migrated `API` block)
//     stay invisible to `config show` / `config set` without needing an
//     allow-list.
//   - Unexported fields are skipped.
func Walk(cfg any, visit LeafVisitor) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return fmt.Errorf("schema.Walk: nil pointer")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("schema.Walk: expected struct or *struct, got %s", v.Kind())
	}
	walkStruct(v, visit)
	return nil
}

func walkStruct(v reflect.Value, visit LeafVisitor) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if key := f.Tag.Get("key"); key != "" {
			visit(LeafField{Path: key, Field: f, Value: fv})
			continue
		}
		if fv.Kind() == reflect.Struct {
			walkStruct(fv, visit)
		}
	}
}

// Keys is a convenience that returns the ordered list of dotted paths Walk
// would yield. Used by tests and by `cix config keys`.
func Keys(cfg any) ([]string, error) {
	var keys []string
	err := Walk(cfg, func(l LeafField) {
		keys = append(keys, l.Path)
	})
	return keys, err
}
