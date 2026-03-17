package cmd

import (
	"fmt"

	"github.com/anthropics/code-index/cli/internal/config"
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

Supported keys:
  api.url       - API server URL
  api.key       - API authentication key
  watcher.debounce_ms - Debounce delay in milliseconds

Examples:
  cix config set api.key cix_abc123...
  cix config set api.url http://localhost:21847
  cix config set watcher.debounce_ms 3000`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
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
	configCmd.AddCommand(configPathCmd)
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Println("Configuration:")
	fmt.Printf("  API URL: %s\n", cfg.API.URL)
	if cfg.API.Key != "" {
		// Mask API key
		maskedKey := cfg.API.Key
		if len(maskedKey) > 20 {
			maskedKey = maskedKey[:12] + "..." + maskedKey[len(maskedKey)-4:]
		}
		fmt.Printf("  API Key: %s\n", maskedKey)
	} else {
		fmt.Printf("  API Key: (not set)\n")
	}

	fmt.Printf("\nWatcher:\n")
	fmt.Printf("  Enabled: %v\n", cfg.Watcher.Enabled)
	fmt.Printf("  Debounce: %dms\n", cfg.Watcher.DebounceMS)

	fmt.Printf("\nIndexing:\n")
	fmt.Printf("  Batch Size: %d files\n", cfg.Indexing.BatchSize)

	fmt.Printf("\nServer:\n")
	fmt.Printf("  Port: %d\n", cfg.Server.Port)
	fmt.Printf("  Cache TTL: %ds\n", cfg.Server.CacheTTL)

	if len(cfg.Projects) > 0 {
		fmt.Printf("\nProjects (%d):\n", len(cfg.Projects))
		for _, p := range cfg.Projects {
			autoWatch := "no"
			if p.AutoWatch {
				autoWatch = "yes"
			}
			fmt.Printf("  - %s (auto-watch: %s)\n", p.Path, autoWatch)
		}
	}

	fmt.Printf("\nConfig file: %s\n", config.GetConfigPath())

	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Set value based on key
	switch key {
	case "api.url":
		cfg.API.URL = value
	case "api.key":
		cfg.API.Key = value
	case "watcher.debounce_ms":
		var ms int
		_, err := fmt.Sscanf(value, "%d", &ms)
		if err != nil {
			return fmt.Errorf("invalid value for debounce_ms: %s", value)
		}
		cfg.Watcher.DebounceMS = ms
	case "indexing.batch_size":
		var bs int
		_, err := fmt.Sscanf(value, "%d", &bs)
		if err != nil || bs < 1 {
			return fmt.Errorf("invalid value for batch_size (must be >= 1): %s", value)
		}
		cfg.Indexing.BatchSize = bs
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	// Save config
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("✓ Set %s = %s\n", key, value)
	return nil
}
