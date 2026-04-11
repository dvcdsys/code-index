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
  watcher.sync_interval_mins - Periodic sync interval in minutes

Examples:
  cix config set api.key cix_abc123...
  cix config set api.url http://localhost:21847
  cix config set watcher.debounce_ms 3000
  cix config set watcher.sync_interval_mins 5`,
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

	apiKey := "(not set)"
	if cfg.API.Key != "" {
		k := cfg.API.Key
		if len(k) > 20 {
			k = k[:12] + "..." + k[len(k)-4:]
		}
		apiKey = k
	}

	fmt.Printf("%-28s = %s\n", "api.url", cfg.API.URL)
	fmt.Printf("%-28s = %s\n", "api.key", apiKey)
	fmt.Printf("%-28s = %v\n", "watcher.enabled", cfg.Watcher.Enabled)
	fmt.Printf("%-28s = %d\n", "watcher.debounce_ms", cfg.Watcher.DebounceMS)
	fmt.Printf("%-28s = %d\n", "watcher.sync_interval_mins", cfg.Watcher.SyncIntervalMins)
	fmt.Printf("%-28s = %d\n", "indexing.batch_size", cfg.Indexing.BatchSize)
	fmt.Printf("%-28s = %d\n", "server.port", cfg.Server.Port)
	fmt.Printf("%-28s = %d\n", "server.cache_ttl", cfg.Server.CacheTTL)

	if len(cfg.Projects) > 0 {
		fmt.Printf("\nprojects (%d):\n", len(cfg.Projects))
		for _, p := range cfg.Projects {
			fmt.Printf("  - %s (auto-watch: %v)\n", p.Path, p.AutoWatch)
		}
	}

	fmt.Printf("\nconfig file: %s\n", config.GetConfigPath())

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
	case "watcher.sync_interval_mins":
		var mins int
		_, err := fmt.Sscanf(value, "%d", &mins)
		if err != nil || mins < 1 {
			return fmt.Errorf("invalid value for sync_interval_mins (must be >= 1): %s", value)
		}
		cfg.Watcher.SyncIntervalMins = mins
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
