package cmd

import (
	"fmt"

	"github.com/dvcdsys/code-index/cli/internal/config"
	"github.com/dvcdsys/code-index/cli/internal/config/tui"
	"github.com/spf13/cobra"
)

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Interactively edit configuration (TUI)",
	Long: `Open the full-screen lazygit-style editor for ~/.cix/config.yaml.

Layout: section list on the left (Servers / Watcher / Indexing /
Projects / Misc), selected section's content on the right, persistent
key-hint bar at the bottom.

Keys (press ? for the full table):
  ↑/k ↓/j        move within a panel
  ←/h →/l / tab  switch panel
  enter          edit selected field
  space / x      toggle bool field
  a / d          add / delete server (Servers section)
  m              mark selected server as default
  t              test connection (server)
  q / esc        quit

Every edit goes through the same validation as 'cix config set' and is
written to disk immediately.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return tui.RunEdit(cfg)
	},
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "First-run wizard (TUI)",
	Long: `Seed a fresh ~/.cix/config.yaml with the localhost default server
and open the interactive editor pointing at it.

If a configuration already exists this is equivalent to 'cix config edit'
— no overwrite; the existing servers, settings, and projects are kept.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return tui.RunInit(cfg)
	},
}

func init() {
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configInitCmd)
}
