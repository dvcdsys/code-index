package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var (
	installName  string
	installPrint bool
)

// mcpHost describes a host application cix can register itself with as a local
// stdio MCP server. Each host stores MCP servers in its own config file and
// its own format, so a host owns two things: where that config lives, and how
// to merge/remove a server entry in that format.
//
// Today only Claude Desktop is implemented (JSON `mcpServers`). This registry
// is the seam for supporting other agents/harnesses later — e.g. Codex, whose
// MCP config is TOML (`[mcp_servers.<name>]` in ~/.codex/config.toml) — by
// adding an entry here with its own path resolver and TOML merge/remove funcs.
// The command wiring (install/uninstall, --print, --name, .bak) stays untouched.
type mcpHost struct {
	name        string // canonical token typed on the command line
	displayName string

	// configPath resolves the host's MCP config file for the current OS.
	configPath func() (string, error)

	// merge adds/updates the cix server entry in the host's existing config
	// bytes (empty = a fresh config) and returns the rewritten config. It must
	// be idempotent and must preserve every other server and setting.
	merge func(existing []byte, serverKey, command string, args []string) ([]byte, error)
	// remove deletes serverKey; the bool reports whether anything was removed.
	remove func(existing []byte, serverKey string) ([]byte, bool, error)
	// preview renders just the entry that merge would add, for --print.
	preview func(serverKey, command string, args []string) (string, error)
	// otherServers lists the OTHER server keys in the config (names only, never
	// values, which may hold secrets), so --print can show they're preserved.
	otherServers func(existing []byte, exclude string) []string
}

// mcpHosts is the registry of supported hosts. Add new agents/harnesses here.
var mcpHosts = []mcpHost{
	{
		name:         "claude-desktop",
		displayName:  "Claude Desktop",
		configPath:   claudeDesktopConfigPath,
		merge:        mcpServersJSONMerge,
		remove:       mcpServersJSONRemove,
		preview:      mcpServersJSONPreview,
		otherServers: mcpServersJSONOtherNames,
	},
}

func lookupHost(arg string) (mcpHost, error) {
	key := strings.ToLower(strings.TrimSpace(arg))
	for _, h := range mcpHosts {
		if h.name == key {
			return h, nil
		}
	}
	return mcpHost{}, fmt.Errorf("unsupported host %q (supported: %s)", arg, strings.Join(hostNames(), ", "))
}

func hostNames() []string {
	names := make([]string, len(mcpHosts))
	for i, h := range mcpHosts {
		names[i] = h.name
	}
	return names
}

// mcpInstallCmd registers THIS already-installed cix binary as an MCP server in
// a host app's config, pointing at it by absolute path. It is the standard way
// to add a local stdio MCP server (the same mechanism docker/npx-based MCP
// servers use) — nothing is bundled or copied, since cix is already on the
// machine. Claude Code already reaches cix via the cix CLI + plugin; this is the
// path for hosts that don't, starting with Claude Desktop.
var mcpInstallCmd = &cobra.Command{
	Use:   "install <host>",
	Short: "Register this cix binary as an MCP server with a host app (host: claude-desktop)",
	Long: `Register this already-installed cix binary as a local MCP server with a host app.

Adds an entry to the host's MCP config whose command points at THIS cix binary
with the "mcp" subcommand, so the host launches "cix mcp" on demand. Other
servers and settings are preserved; a .bak of the previous config is kept. The
cix server URL and API key come from ~/.cix/config.yaml, so no secrets are
written into the host config.

Claude Code already reaches cix through the cix CLI + plugin, so this is its
Claude Desktop counterpart — and Cowork, which runs inside Claude Desktop, picks
the tools up too.

Supported hosts:
  claude-desktop    Claude Desktop (~/Library/Application Support/Claude on macOS)

Examples:
  cix mcp install claude-desktop            # register, then restart Claude Desktop
  cix mcp install claude-desktop --print    # show what would be added, change nothing
  cix mcp install claude-desktop --name cix-prod`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPInstall,
}

// mcpUninstallCmd removes a previously-written registration.
var mcpUninstallCmd = &cobra.Command{
	Use:   "uninstall <host>",
	Short: "Remove cix's MCP registration from a host app (host: claude-desktop)",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPUninstall,
}

func init() {
	mcpCmd.AddCommand(mcpInstallCmd)
	mcpCmd.AddCommand(mcpUninstallCmd)
	mcpInstallCmd.Flags().StringVar(&installName, "name", "cix", "key to register the server under in the host config")
	mcpInstallCmd.Flags().BoolVar(&installPrint, "print", false, "print the entry that would be added instead of writing it")
	mcpUninstallCmd.Flags().StringVar(&installName, "name", "cix", "server key to remove from the host config")
}

func runMCPInstall(_ *cobra.Command, args []string) error {
	host, err := lookupHost(args[0])
	if err != nil {
		return err
	}
	cfgPath, err := host.configPath()
	if err != nil {
		return err
	}
	exe, err := selfExecutablePath()
	if err != nil {
		return err
	}

	existing, rerr := os.ReadFile(cfgPath)
	if rerr != nil && !os.IsNotExist(rerr) {
		return fmt.Errorf("read %s: %w", cfgPath, rerr)
	}

	if installPrint {
		// Show ONLY the entry we'd add plus the names of preserved servers —
		// never the whole file, which may hold other servers' secret env vars.
		entry, perr := host.preview(installName, exe, []string{"mcp"})
		if perr != nil {
			return perr
		}
		fmt.Printf("Host:   %s\n", host.displayName)
		fmt.Printf("Target: %s\n\n", cfgPath)
		fmt.Printf("Would add/update the %q MCP server:\n%s\n", installName, entry)
		if host.otherServers != nil {
			if others := host.otherServers(existing, installName); len(others) > 0 {
				fmt.Printf("\nOther servers preserved unchanged: %s\n", strings.Join(others, ", "))
			}
		}
		return nil
	}

	out, err := host.merge(existing, installName, exe, []string{"mcp"})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	// The host config can hold OTHER MCP servers' secrets (API keys in their
	// env blocks), so don't widen its permissions: preserve the existing
	// file's mode, and create a fresh one private (0o600) rather than 0o644.
	mode := configFileMode(cfgPath)
	if len(existing) > 0 {
		_ = os.WriteFile(cfgPath+".bak", existing, mode) // best-effort backup
	}
	if err := writeFileMode(cfgPath, out, mode); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	fmt.Printf("Registered cix as MCP server %q with %s:\n  %s\n", installName, host.displayName, cfgPath)
	fmt.Printf("  command: %s mcp\n\n", exe)
	fmt.Printf("Restart %s to load it; the cix tools appear once it reconnects.\n", host.displayName)
	return nil
}

func runMCPUninstall(_ *cobra.Command, args []string) error {
	host, err := lookupHost(args[0])
	if err != nil {
		return err
	}
	cfgPath, err := host.configPath()
	if err != nil {
		return err
	}
	existing, rerr := os.ReadFile(cfgPath)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			fmt.Printf("Nothing to do — no config at %s\n", cfgPath)
			return nil
		}
		return fmt.Errorf("read %s: %w", cfgPath, rerr)
	}
	out, removed, err := host.remove(existing, installName)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Printf("Nothing to do — no server %q in %s\n", installName, cfgPath)
		return nil
	}
	if err := writeFileMode(cfgPath, out, configFileMode(cfgPath)); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Printf("Removed MCP server %q from %s. Restart %s.\n", installName, cfgPath, host.displayName)
	return nil
}

// configFileMode returns the permission bits to write a host config with. An
// existing file's mode is preserved; a not-yet-existing one is created private
// (0o600) because the config sits next to other servers' secret env values.
func configFileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o600
}

// writeFileMode writes data to path and enforces mode even when the file
// already exists (os.WriteFile only applies perm bits on creation), so a
// previously world-readable config is tightened rather than left as-is.
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// selfExecutablePath returns the absolute, symlink-resolved path of the running
// cix binary, so the host launches the exact binary the user ran `install` with
// (handling the common case where `cix` on PATH is a symlink to a build).
func selfExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate cix binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return exe, nil
}

// claudeDesktopConfigPath returns the per-OS path of Claude Desktop's MCP
// config file.
func claudeDesktopConfigPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json"), nil
	default:
		cfgHome := os.Getenv("XDG_CONFIG_HOME")
		if cfgHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			cfgHome = filepath.Join(home, ".config")
		}
		return filepath.Join(cfgHome, "Claude", "claude_desktop_config.json"), nil
	}
}

// ---- Claude Desktop config format: JSON `mcpServers` ------------------------
//
// These implement the mcpHost merge/remove/preview funcs for hosts that use the
// Claude Desktop shape — a JSON object with an `mcpServers` map of name → entry.
// A future TOML-based host (e.g. Codex) supplies its own equivalents.

// mcpServersJSONMerge merges a `mcpServers.<serverKey>` entry into an existing
// host config (or an empty one), preserving every other key and server. The
// result is stable, indented JSON. Running it twice is idempotent.
func mcpServersJSONMerge(existing []byte, serverKey, command string, args []string) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("existing config is not valid JSON: %w", err)
		}
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[serverKey] = mcpServerEntry(command, args)
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// mcpServersJSONRemove removes mcpServers.<serverKey>. The bool reports whether
// anything was removed.
func mcpServersJSONRemove(existing []byte, serverKey string) ([]byte, bool, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, false, fmt.Errorf("existing config is not valid JSON: %w", err)
		}
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return existing, false, nil
	}
	if _, ok := servers[serverKey]; !ok {
		return existing, false, nil
	}
	delete(servers, serverKey)
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// mcpServersJSONPreview renders just the entry mcpServersJSONMerge would add, in
// the Claude Desktop `mcpServers` JSON shape.
func mcpServersJSONPreview(serverKey, command string, args []string) (string, error) {
	entry, err := json.MarshalIndent(
		map[string]any{serverKey: mcpServerEntry(command, args)}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(entry), nil
}

// mcpServerEntry builds a single mcpServers entry: {command, args}.
func mcpServerEntry(command string, args []string) map[string]any {
	entry := map[string]any{"command": command}
	if len(args) > 0 {
		a := make([]any, len(args))
		for i, s := range args {
			a[i] = s
		}
		entry["args"] = a
	}
	return entry
}

// mcpServersJSONOtherNames lists the mcpServers keys in existing config except
// exclude, sorted — names only, never values (which may hold secrets).
func mcpServersJSONOtherNames(existing []byte, exclude string) []string {
	if len(bytes.TrimSpace(existing)) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(existing, &root); err != nil {
		return nil
	}
	servers, _ := root["mcpServers"].(map[string]any)
	var names []string
	for k := range servers {
		if k != exclude {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}
