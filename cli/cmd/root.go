package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dvcdsys/code-index/cli/internal/client"
	"github.com/dvcdsys/code-index/cli/internal/config"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags "-X cmd.Version=v0.1.0"
var Version = "dev"

var bannerOnce bool

func printBanner() {
	if bannerOnce {
		return
	}
	bannerOnce = true

	// Only print to a real terminal — skip when piped or called by agents.
	fi, err := os.Stdout.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return
	}

	fmt.Printf("\033[96m")
	fmt.Print(` ██████╗██╗██╗  ██╗
██╔════╝██║╚██╗██╔╝
██║     ██║ ╚███╔╝
██║     ██║ ██╔██╗
╚██████╗██║██╔╝ ██╗
 ╚═════╝╚═╝╚═╝  ╚═╝`)
	fmt.Printf("\033[0m  \033[2mCode IndeX %s\033[0m\n\n", Version)
}

var (
	cfgFile    string
	apiURL     string
	apiKey     string
	serverName string
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "cix",
	Short: "Code IndeX — search your codebase by meaning",
	Long: `cix (Code IndeX) — semantic code search powered by embeddings and AST parsing.

Search by meaning, not just text. Works with any agent or terminal.
Files are automatically re-indexed when changed via the file watcher.`,
	Run: func(cmd *cobra.Command, args []string) {
		if showVersion, _ := cmd.Flags().GetBool("version"); showVersion {
			fmt.Printf("cix %s\n", Version)
			return
		}
		printBanner()
		cmd.Help()
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverName, "server", "", "named server alias from config (default: the configured default server)")
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "", "API server URL (overrides the selected server's URL)")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key (overrides the selected server's key)")
}

// resolveProjectByName performs an exact-match lookup of name against the
// HostPath of every registered project. Unlike findProjectRoot it does not
// run filepath.Abs or any prefix walk — the caller is asserting "use this
// exact registered project, no magic". On miss the error lists every
// registered HostPath so the user can copy/paste the right one.
func resolveProjectByName(name string, apiClient *client.Client) (string, error) {
	projects, err := apiClient.ListProjects()
	if err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}
	registered := make([]string, 0, len(projects))
	for _, p := range projects {
		if p.HostPath == name {
			return p.HostPath, nil
		}
		registered = append(registered, p.HostPath)
	}
	if len(registered) == 0 {
		return "", fmt.Errorf("project %q not found; no projects are registered (run `cix init` or attach a repo via the dashboard)", name)
	}
	return "", fmt.Errorf("project %q not found; registered projects:\n  - %s", name, strings.Join(registered, "\n  - "))
}

// findProjectRoot resolves a candidate path to a registered project root.
//
// If the candidate path exactly matches a registered project it is returned as-is.
// Otherwise the function looks for the longest registered project path that is
// a prefix of the candidate — the same way git finds the repo root when you are
// inside a subdirectory.
//
// If no match is found the original candidate is returned so the API can
// produce a meaningful "project not found" error.
func findProjectRoot(candidatePath string, apiClient *client.Client) string {
	projects, err := apiClient.ListProjects()
	if err != nil {
		return candidatePath
	}

	best := ""
	for _, p := range projects {
		root := p.HostPath
		if candidatePath == root {
			return root
		}
		if strings.HasPrefix(candidatePath, root+"/") && len(root) > len(best) {
			best = root
		}
	}

	if best != "" {
		return best
	}
	return candidatePath
}

// Env-var names recognised by the CLI for server selection / overrides.
// Precedence is always flag > env > config-file > default. The CIX_*
// surface is deliberately tiny — three vars, all about reaching a server.
// Everything else lives in ~/.cix/config.yaml.
const (
	envServer = "CIX_SERVER"
	envAPIURL = "CIX_API_URL"
	envAPIKey = "CIX_API_KEY"
)

// getClient creates an API client from config / flags / env.
//
// Precedence per axis:
//   - target server alias:  --server > CIX_SERVER > default_server
//   - server URL override:  --api-url > CIX_API_URL > the resolved server's URL
//   - server key override:  --api-key > CIX_API_KEY > the resolved server's key
//
// The env vars override the *resolved* server's URL/key locally — they
// never mutate the in-memory ServerEntry, so a follow-up config.Save() will
// not persist them. This matches the flag behavior and is what users in CI
// expect: `CIX_API_KEY=secret cix search …` must not write the secret back
// to ~/.cix/config.yaml.
func getClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Server alias: flag > env > default. Read env only when the flag is
	// empty; the flag is the authoritative override.
	name := serverName
	if name == "" {
		name = os.Getenv(envServer)
	}
	srv, err := cfg.ResolveServer(name)
	if err != nil {
		return nil, err
	}

	// URL override: flag > env > entry.
	url := apiURL
	if url == "" {
		url = os.Getenv(envAPIURL)
	}
	if url == "" {
		url = srv.URL
	}

	// Key override: flag > env > entry. Local copy — never write back.
	key := apiKey
	if key == "" {
		key = os.Getenv(envAPIKey)
	}
	if key == "" {
		key = srv.Key
	}
	if key == "" {
		return nil, fmt.Errorf("API key not set for server %q. Use --api-key flag, set %s=…, or run 'cix config set server.%s.key <key>'", srv.Name, envAPIKey, srv.Name)
	}

	c := client.New(url, key)

	// Custom headers: ${ENV}-expand each value into a local copy (never
	// written back to disk, like the url/key overrides above) so secrets can
	// stay in the environment rather than in config.yaml. Expansion is strict —
	// a referenced-but-unset variable is an error, not a silent empty header —
	// and validates after expansion. All failures name the header/variable but
	// never echo the resolved value.
	if len(srv.Headers) > 0 {
		expanded := make(map[string]string, len(srv.Headers))
		for name, raw := range srv.Headers {
			val, err := config.ExpandEnvHeaderValue(raw)
			if err != nil {
				return nil, fmt.Errorf("custom header %q for server %q: %w", name, srv.Name, err)
			}
			if err := config.ValidateHeader(name, val); err != nil {
				return nil, fmt.Errorf("invalid custom header %q for server %q: %w", name, srv.Name, err)
			}
			expanded[name] = val
		}
		c.SetCustomHeaders(expanded)
	}

	if cfg.Indexing.StreamingIdleTimeoutSec > 0 {
		c.SetStreamingIdleTimeout(time.Duration(cfg.Indexing.StreamingIdleTimeoutSec) * time.Second)
	}
	return c, nil
}
