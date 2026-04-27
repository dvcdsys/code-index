package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/code-index/cli/internal/client"
	"github.com/anthropics/code-index/cli/internal/config"
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
	cfgFile string
	apiURL  string
	apiKey  string
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
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "", "API server URL (default from config)")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key (default from config)")
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

// getClient creates an API client from config or flags
func getClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	url := apiURL
	if url == "" {
		url = cfg.API.URL
	}

	key := apiKey
	if key == "" {
		key = cfg.API.Key
		if key == "" {
			return nil, fmt.Errorf("API key not set. Use --api-key flag or run 'cix config set api.key <key>'")
		}
	}

	return client.New(url, key), nil
}
