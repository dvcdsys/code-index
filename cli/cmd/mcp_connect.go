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
	connectName  string
	connectPrint bool
)

// mcpConnectCmd registers THIS already-installed cix binary as an MCP server in
// the host's config, pointing at it by absolute path. It is the standard way to
// add a local stdio MCP server (the same mechanism docker/npx-based MCP servers
// use) — nothing is bundled or copied, since cix is already on the machine.
var mcpConnectCmd = &cobra.Command{
	Use:   "connect <host>",
	Short: "Register this cix binary as an MCP server with a host app (host: claude)",
	Long: `Register this already-installed cix binary as an MCP server with a host app.

Adds an entry to the host's MCP config (mcpServers) whose command points at THIS
cix binary with the "mcp" subcommand, so the host launches "cix mcp" on demand.
Other servers and settings are preserved; a .bak of the previous config is kept.
The cix server URL and API key come from ~/.cix/config.yaml, so no secrets are
written into the host config.

Supported hosts:
  claude    Claude Desktop (~/Library/Application Support/Claude on macOS)

Examples:
  cix mcp connect claude            # register, then restart Claude Desktop
  cix mcp connect claude --print    # show what would be added, change nothing
  cix mcp connect claude --name cix-prod`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPConnect,
}

// mcpDisconnectCmd removes a previously-written registration.
var mcpDisconnectCmd = &cobra.Command{
	Use:   "disconnect <host>",
	Short: "Remove cix's MCP registration from a host app (host: claude)",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPDisconnect,
}

func init() {
	mcpCmd.AddCommand(mcpConnectCmd)
	mcpCmd.AddCommand(mcpDisconnectCmd)
	mcpConnectCmd.Flags().StringVar(&connectName, "name", "cix", "key to register the server under in mcpServers")
	mcpConnectCmd.Flags().BoolVar(&connectPrint, "print", false, "print the entry that would be added instead of writing it")
	mcpDisconnectCmd.Flags().StringVar(&connectName, "name", "cix", "server key to remove from mcpServers")
}

func runMCPConnect(_ *cobra.Command, args []string) error {
	host := strings.ToLower(strings.TrimSpace(args[0]))
	if host != "claude" {
		return fmt.Errorf("unsupported host %q (supported: claude)", host)
	}

	cfgPath, err := claudeDesktopConfigPath()
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

	if connectPrint {
		// Show ONLY the entry we'd add plus the names of preserved servers —
		// never the whole file, which may hold other servers' secret env vars.
		entry, merr := json.MarshalIndent(
			map[string]any{connectName: mcpServerEntry(exe, []string{"mcp"})}, "", "  ")
		if merr != nil {
			return merr
		}
		fmt.Printf("Target: %s\n\n", cfgPath)
		fmt.Printf("Would add/update under mcpServers:\n%s\n", entry)
		if others := otherServerNames(existing, connectName); len(others) > 0 {
			fmt.Printf("\nOther servers preserved unchanged: %s\n", strings.Join(others, ", "))
		}
		return nil
	}

	out, err := mcpConnectConfig(existing, connectName, exe, []string{"mcp"})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if len(existing) > 0 {
		_ = os.WriteFile(cfgPath+".bak", existing, 0o644) // best-effort backup
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	fmt.Printf("Registered cix as MCP server %q:\n  %s\n", connectName, cfgPath)
	fmt.Printf("  command: %s mcp\n\n", exe)
	fmt.Println("Restart Claude Desktop to load it; the cix tools appear once it reconnects.")
	return nil
}

func runMCPDisconnect(_ *cobra.Command, args []string) error {
	host := strings.ToLower(strings.TrimSpace(args[0]))
	if host != "claude" {
		return fmt.Errorf("unsupported host %q (supported: claude)", host)
	}
	cfgPath, err := claudeDesktopConfigPath()
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
	out, removed, err := mcpDisconnectConfig(existing, connectName)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Printf("Nothing to do — no server %q in %s\n", connectName, cfgPath)
		return nil
	}
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Printf("Removed MCP server %q from %s. Restart Claude Desktop.\n", connectName, cfgPath)
	return nil
}

// selfExecutablePath returns the absolute, symlink-resolved path of the running
// cix binary, so the host launches the exact binary the user ran `connect` with
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

// mcpConnectConfig merges a `mcpServers.<serverKey>` entry into an existing host
// config (or an empty one), preserving every other key and server. The result
// is stable, indented JSON. Running it twice is idempotent.
func mcpConnectConfig(existing []byte, serverKey, command string, args []string) ([]byte, error) {
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

// mcpDisconnectConfig removes mcpServers.<serverKey>. The bool reports whether
// anything was removed.
func mcpDisconnectConfig(existing []byte, serverKey string) ([]byte, bool, error) {
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

// otherServerNames lists the mcpServers keys in existing config except exclude,
// sorted — names only, never values (which may hold secrets).
func otherServerNames(existing []byte, exclude string) []string {
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
