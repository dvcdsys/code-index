package tui

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/config/schema"
)

// row is one entry rendered in the right panel. The TUI keeps every
// section's rows in this uniform shape so navigation, rendering, and
// edit-mode dispatch share one code path.
type row struct {
	label string // shown in the "key" column
	value string // shown in the "value" column (formatted for display)

	// dispatch on activation
	kind      rowKind
	schemaKey string // for kindScalarEdit / kindBoolToggle: schema dotted key
	serverIdx int    // for kindServerEdit: index in cfg.Servers

	// metadata
	sensitive bool
}

type rowKind int

const (
	rowKindInert        rowKind = iota // no action on Enter
	rowKindScalarEdit                  // Enter opens text input; SetByPath on save
	rowKindBoolToggle                  // space/x flips bool; Enter does too
	rowKindServerEdit                  // Enter opens server URL/key editor
	rowKindDefaultPick                 // Enter cycles default_server alias
)

// rowsFor returns the rendered rows for the currently selected section.
// Pure function of cfg — easy to unit-test without spinning up bubbletea.
func rowsFor(cfg *config.Config, sec sectionID) []row {
	switch sec {
	case secServers:
		return serverRows(cfg)
	case secWatcher:
		return scalarRows(cfg, "watcher.")
	case secIndexing:
		return scalarRows(cfg, "indexing.")
	case secProjects:
		return projectRows(cfg)
	case secMisc:
		return miscRows(cfg)
	}
	return nil
}

func serverRows(cfg *config.Config) []row {
	if len(cfg.Servers) == 0 {
		return nil
	}
	out := make([]row, 0, len(cfg.Servers)+1)
	for i, s := range cfg.Servers {
		// Marker shows which entry is the default. The key field is
		// rendered via Sensitive — never the raw value.
		marker := " "
		if s.Name == cfg.DefaultServer {
			marker = "●"
		}
		keyStatus := "(not set)"
		if s.Key != "" {
			keyStatus = "(set)"
		}
		out = append(out, row{
			label:     fmt.Sprintf("%s %s", marker, s.Name),
			value:     fmt.Sprintf("%s   key %s", s.URL, keyStatus),
			kind:      rowKindServerEdit,
			serverIdx: i,
		})
	}
	out = append(out, row{
		label:     "default_server",
		value:     cfg.DefaultServer,
		kind:      rowKindDefaultPick,
		schemaKey: "default_server",
	})
	return out
}

// scalarRows walks the schema and emits one row per leaf whose dotted
// key starts with prefix. Slice-of-struct leaves (servers, projects)
// are skipped — they have dedicated sections.
func scalarRows(cfg *config.Config, prefix string) []row {
	var out []row
	_ = schema.Walk(cfg, func(l schema.LeafField) {
		if !strings.HasPrefix(l.Path, prefix) {
			return
		}
		// Skip non-scalar leaves (slice-of-struct never has a path prefix
		// in this set, but be defensive).
		if l.Value.Kind() == reflect.Slice && l.Value.Type().Elem().Kind() == reflect.Struct {
			return
		}
		out = append(out, row{
			label:     strings.TrimPrefix(l.Path, prefix),
			value:     formatLeafForRow(l),
			kind:      kindForLeaf(l),
			schemaKey: l.Path,
			sensitive: l.Sensitive(),
		})
	})
	return out
}

func kindForLeaf(l schema.LeafField) rowKind {
	if l.Value.Kind() == reflect.Bool {
		return rowKindBoolToggle
	}
	return rowKindScalarEdit
}

// formatLeafForRow turns the leaf's current value into a display string
// that fits in one terminal row. Sensitive leaves never expose the value.
func formatLeafForRow(l schema.LeafField) string {
	if l.Sensitive() {
		if l.Value.IsZero() {
			return "(not set)"
		}
		return "(set)"
	}
	v := l.Value
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return "✓ enabled"
		}
		return "✗ disabled"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.String:
		return v.String()
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			items := v.Interface().([]string)
			return fmt.Sprintf("[%d items] %s", len(items), strings.Join(truncList(items, 3), ", "))
		}
	}
	return fmt.Sprintf("%v", v.Interface())
}

func truncList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	out := make([]string, 0, n+1)
	out = append(out, xs[:n]...)
	out = append(out, fmt.Sprintf("…+%d", len(xs)-n))
	return out
}

func projectRows(cfg *config.Config) []row {
	out := make([]row, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		wState := "✗"
		if p.AutoWatch {
			wState = "✓"
		}
		out = append(out, row{
			label: p.Path,
			value: fmt.Sprintf("auto-watch %s", wState),
			kind:  rowKindInert,
		})
	}
	return out
}

func miscRows(cfg *config.Config) []row {
	return []row{
		{
			label: "config file",
			value: config.GetConfigPath(),
			kind:  rowKindInert,
		},
	}
}

// rawScalarValue returns the leaf's current value as the canonical string
// the user will see when entering edit mode. For ints/bools we round-trip
// through strconv so the user can edit the exact form SetByPath expects.
func rawScalarValue(l schema.LeafField) string {
	if l.Sensitive() {
		// Edit mode does not pre-fill secrets — the user types fresh.
		return ""
	}
	v := l.Value
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.String:
		return v.String()
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			return strings.Join(v.Interface().([]string), ",")
		}
	}
	return fmt.Sprintf("%v", v.Interface())
}

// findLeaf walks the schema and returns the leaf at path. Used to look
// up a leaf's reflect.Value when committing an edit (we cannot keep a
// stale leaf reference across an Update call because the underlying
// reflect.Value may move when cfg is mutated).
func findLeaf(cfg *config.Config, path string) (schema.LeafField, bool) {
	var found schema.LeafField
	var ok bool
	_ = schema.Walk(cfg, func(l schema.LeafField) {
		if ok || l.Path != path {
			return
		}
		found = l
		ok = true
	})
	return found, ok
}
