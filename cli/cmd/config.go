package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/dvcdsys/code-index/cli/internal/config"
	"github.com/dvcdsys/code-index/cli/internal/config/schema"
	"github.com/spf13/cobra"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration",
	Long:  `View and modify cix configuration stored in ~/.cix/config.yaml`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	RunE:  runConfigShow,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Run 'cix config keys' to list every settable key with its description,
default, and env-var override. Beyond those schema keys, three patterns
manage the multi-server layout:

  server.<name>.url            URL of a named server (creates the entry if absent)
  server.<name>.key            API key of a named server
  server.<name>.header.<Name>  custom HTTP header sent on every request
  default_server               which server is used when --server is omitted
  api.url / api.key            legacy aliases — operate on the default server

Custom headers let the CLI pass an authenticating reverse proxy in front of
cix (e.g. a Cloudflare Access service token). Values support ${ENV} expansion
at request time, so secrets stay out of the config file.

List-valued keys (e.g. watcher.exclude) use comma-separated input with
REPLACE semantics: 'cix config set watcher.exclude "node_modules,vendor"'
overwrites the entire list. There is no 'add'/'append' form.

Examples:
  cix config set server.corporate.url https://cix.corp.internal
  cix config set server.corporate.key cix_abc123...
  cix config set default_server corporate

  # custom headers (e.g. Cloudflare Access service token):
  cix config set server.corporate.header.CF-Access-Client-Id "<id>.access"
  cix config set server.corporate.header.CF-Access-Client-Secret '${CIX_CF_ACCESS_SECRET}'

  cix config set api.url http://localhost:21847        # legacy alias
  cix config set api.key cix_abc123...                 # legacy alias

  cix config set watcher.enabled false                 # bool
  cix config set watcher.debounce_ms 3000              # int
  cix config set watcher.exclude "node_modules,.git"   # list (replace)`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Remove a server or clear a server key",
	Long: `Remove configuration entries.

Supported keys:
  server.<name>                - remove the named server entirely
  server.<name>.key            - clear the named server's API key
  server.<name>.header.<Name>  - remove a custom HTTP header

Examples:
  cix config unset server.corporate
  cix config unset server.corporate.key
  cix config unset server.corporate.header.CF-Access-Client-Id`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigUnset,
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Show config file path",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(config.GetConfigPath())
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configUnsetCmd)
	configCmd.AddCommand(configPathCmd)
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return renderConfigShow(os.Stdout, cfg, config.GetConfigPath())
}

// renderConfigShow writes the human-readable config dump to w.
//
// Exported-via-tests (lowercase but reachable from cmd_test) so the golden-
// file test in config_show_test.go can compare against a fixture without
// shelling out to the CLI binary.
//
// The leaf list is driven by schema.Walk over the Config struct, so any
// new tagged field appears here automatically — no printf drift.
func renderConfigShow(w io.Writer, cfg *config.Config, cfgPath string) error {
	// 1) Servers list — slice of structs, custom renderer.
	renderServersBlock(w, cfg)

	// 2) Scalar leaves grouped by top-level prefix (watcher.* / server.* /
	//    indexing.*) with a blank line between groups for readability.
	var lastGroup string
	first := true
	err := schema.Walk(cfg, func(l schema.LeafField) {
		// servers / projects are slice leaves rendered separately.
		if l.Path == "servers" || l.Path == "projects" {
			return
		}
		group := topGroup(l.Path)
		if !first && group != lastGroup {
			fmt.Fprintln(w)
		}
		first = false
		lastGroup = group
		renderScalarLeaf(w, l)
	})
	if err != nil {
		return fmt.Errorf("walk schema: %w", err)
	}

	// 3) Projects (slice).
	if len(cfg.Projects) > 0 {
		fmt.Fprintf(w, "\nprojects (%d):\n", len(cfg.Projects))
		for _, p := range cfg.Projects {
			fmt.Fprintf(w, "  - %s (auto-watch: %v)\n", p.Path, p.AutoWatch)
		}
	}

	fmt.Fprintf(w, "\nconfig file: %s\n", cfgPath)
	return nil
}

func renderServersBlock(w io.Writer, cfg *config.Config) {
	fmt.Fprintf(w, "servers (%d):\n", len(cfg.Servers))
	for _, s := range cfg.Servers {
		// Render only "set" / "not set" for the key — never any data derived
		// from it. CodeQL go/clear-text-logging flags partial/masked/length
		// output and even local variables named `*Key`/`*Secret` (sensitive-
		// name heuristic), so the status string is named `keyStatus`.
		keyStatus := "(not set)"
		if s.Key != "" {
			keyStatus = "(set)"
		}
		marker := "  "
		if s.Name == cfg.DefaultServer {
			marker = "* "
		}
		fmt.Fprintf(w, "%s%-16s url=%s key=%s", marker, s.Name, s.URL, keyStatus)
		// Surface custom headers by COUNT only — values may be secrets and must
		// never be printed. Omitted entirely when none are set.
		if n := len(s.Headers); n > 0 {
			fmt.Fprintf(w, " headers=%d", n)
		}
		fmt.Fprintln(w)
	}
}

// keyWidth is the column width for the "key = value" lines. Wide enough for
// the longest current path ("indexing.streaming_idle_timeout_sec" = 34 chars)
// plus one space of slack so future additions don't force a re-tune.
const keyWidth = 36

func renderScalarLeaf(w io.Writer, l schema.LeafField) {
	if l.Sensitive() {
		// Tag-gated branch: we only inspect *whether* the value is empty via
		// reflect.Value.IsZero(), and the resulting string is named
		// `keyStatus` to satisfy CodeQL's sensitive-name heuristic. The
		// underlying value never lands in a named *Key/*Secret variable.
		keyStatus := "(not set)"
		if !l.Value.IsZero() {
			keyStatus = "(set)"
		}
		fmt.Fprintf(w, "%-*s = %s\n", keyWidth, l.Path, keyStatus)
		return
	}

	v := l.Value
	switch v.Kind() {
	case reflect.Bool:
		fmt.Fprintf(w, "%-*s = %v\n", keyWidth, l.Path, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fmt.Fprintf(w, "%-*s = %d\n", keyWidth, l.Path, v.Int())
	case reflect.String:
		fmt.Fprintf(w, "%-*s = %s\n", keyWidth, l.Path, v.String())
	case reflect.Slice:
		fmt.Fprintf(w, "%-*s = %v\n", keyWidth, l.Path, v.Interface())
	default:
		fmt.Fprintf(w, "%-*s = %v\n", keyWidth, l.Path, v.Interface())
	}
}

// topGroup returns the prefix of a dotted key up to the first dot, used to
// group related keys with blank lines in the show output. "default_server"
// (no dot) groups under itself.
func topGroup(path string) string {
	if i := strings.IndexByte(path, '.'); i > 0 {
		return path[:i]
	}
	return path
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Server-management keys persist on their own (they may create entries and
	// reassign the default), so handle them before the schema setter — those
	// side effects don't fit the "parse and assign one field" model.
	switch {
	case key == "default_server":
		if err := config.SetDefaultServer(value); err != nil {
			return err
		}
		fmt.Printf("✓ Set %s = %s\n", key, value)
		return nil
	case key == "api.url":
		// Legacy alias: operate on the default server.
		name := defaultServerName(cfg)
		if err := config.SetServerURL(name, value); err != nil {
			return err
		}
		fmt.Printf("✓ Set %s = %s (server %q)\n", key, value, name)
		return nil
	case key == "api.key":
		name := defaultServerName(cfg)
		if err := config.SetServerKey(name, value); err != nil {
			return err
		}
		fmt.Printf("✓ Set %s (server %q)\n", key, name)
		return nil
	case strings.HasPrefix(key, "server."):
		// Header form: server.<name>.header.<HeaderName> (HeaderName may itself
		// contain dots, so it is everything after the 3rd segment).
		if name, headerName, ok := parseServerHeaderKey(key); ok {
			if err := config.SetServerHeader(name, headerName, value); err != nil {
				return err
			}
			// Never echo the value — header values may be secrets.
			fmt.Printf("✓ Set server.%s.header.%s\n", name, headerName)
			return nil
		}
		name, field, perr := parseServerKey(key)
		if perr != nil {
			return perr
		}
		switch field {
		case "url":
			if err := config.SetServerURL(name, value); err != nil {
				return err
			}
		case "key":
			if err := config.SetServerKey(name, value); err != nil {
				return err
			}
		}
		fmt.Printf("✓ Set %s\n", key)
		return nil
	}

	// Schema-driven setter: handles every leaf annotated with a `key:` tag
	// (watcher.*, indexing.*, server.*, default_server). Validates and
	// persists internally. Unknown keys surface a clear error — the legacy
	// hand-rolled switch was removed in step 10 of the config refactor; its
	// surface is now a strict subset of the schema's.
	if err := config.SetByPath(key, value); err != nil {
		if errors.Is(err, config.ErrUnknownKey) {
			return fmt.Errorf("unknown config key %q (run 'cix config keys' for the full list)", key)
		}
		return err
	}
	fmt.Printf("✓ Set %s = %s\n", key, value)
	return nil
}

func runConfigUnset(cmd *cobra.Command, args []string) error {
	key := args[0]

	if !strings.HasPrefix(key, "server.") {
		return fmt.Errorf("unknown unset key: %s (supported: server.<name>, server.<name>.key)", key)
	}

	// Header form: server.<name>.header.<HeaderName>.
	if name, headerName, ok := parseServerHeaderKey(key); ok {
		if err := config.UnsetServerHeader(name, headerName); err != nil {
			return err
		}
		fmt.Printf("✓ Removed header %q for server %q\n", headerName, name)
		return nil
	}

	rest := strings.TrimPrefix(key, "server.")
	switch {
	case strings.HasSuffix(rest, ".key"):
		name := strings.TrimSuffix(rest, ".key")
		if name == "" || strings.Contains(name, ".") {
			return fmt.Errorf("invalid server key: %s", key)
		}
		if err := config.SetServerKey(name, ""); err != nil {
			return err
		}
		fmt.Printf("✓ Cleared key for server %q\n", name)
		return nil
	case !strings.Contains(rest, "."):
		// `server.<name>` — remove the whole server.
		reassigned, err := config.RemoveServer(rest)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Removed server %q\n", rest)
		if reassigned != "" {
			fmt.Printf("  default server is now %q\n", reassigned)
		}
		return nil
	default:
		return fmt.Errorf("unknown unset key: %s (supported: server.<name>, server.<name>.key)", key)
	}
}

// defaultServerName returns the name of the default server for legacy api.*
// aliases, falling back to the canonical "default" name when none is set.
func defaultServerName(cfg *config.Config) string {
	if s, ok := cfg.DefaultServerEntry(); ok {
		return s.Name
	}
	return config.DefaultServerName
}

// parseServerHeaderKey recognises the `server.<name>.header.<HeaderName>` form
// and splits it into the server name and header name. HeaderName is everything
// after the literal `header.` segment, so it may itself contain dots. Returns
// ok=false for any other shape (so callers fall through to parseServerKey).
func parseServerHeaderKey(key string) (name, headerName string, ok bool) {
	parts := strings.SplitN(key, ".", 4)
	if len(parts) != 4 || parts[0] != "server" || parts[1] == "" || parts[2] != "header" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// parseServerKey splits a `server.<name>.<field>` config key into its name and
// field (url|key), validating the shape.
func parseServerKey(key string) (name, field string, err error) {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) != 3 || parts[0] != "server" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid server key %q (expected server.<name>.url or server.<name>.key)", key)
	}
	name, field = parts[1], parts[2]
	if field != "url" && field != "key" {
		return "", "", fmt.Errorf("invalid server field %q in %q (expected url or key)", field, key)
	}
	return name, field, nil
}
