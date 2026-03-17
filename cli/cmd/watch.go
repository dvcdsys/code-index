package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/daemon"
	"github.com/anthropics/code-index/cli/internal/watcher"
	"github.com/spf13/cobra"
)

var (
	watchForeground bool
	watchDaemonMode bool
	watchStopAll    bool
)

var watchCmd = &cobra.Command{
	Use:   "watch [path]",
	Short: "Start file watcher for auto-reindexing",
	Long: `Start watching a project directory for file changes.
When files change, triggers incremental reindexing on the server.

By default runs as a background daemon. Use --foreground to run in terminal.

Examples:
  cix watch                    # Start daemon for current directory
  cix watch /path/to/project   # Start daemon for specific project
  cix watch --foreground       # Run in terminal (Ctrl+C to stop)
  cix watch stop               # Stop daemon for current directory
  cix watch stop --all         # Stop all daemons
  cix watch status             # Check daemon for current directory
  cix watch list               # List all running daemons`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWatch,
}

var watchStopCmd = &cobra.Command{
	Use:   "stop [path]",
	Short: "Stop file watcher daemon",
	Long: `Stop the watcher daemon for a project.
Without arguments, stops the daemon for the current directory.

Examples:
  cix watch stop                  # Stop daemon for current dir
  cix watch stop /path/to/project # Stop daemon for specific project
  cix watch stop --all            # Stop all daemons`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWatchStop,
}

var watchStatusCmd = &cobra.Command{
	Use:   "status [path]",
	Short: "Check file watcher daemon status",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWatchStatus,
}

var watchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all running file watcher daemons",
	RunE:  runWatchList,
}

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.AddCommand(watchStopCmd)
	watchCmd.AddCommand(watchStatusCmd)
	watchCmd.AddCommand(watchListCmd)

	watchCmd.Flags().BoolVarP(&watchForeground, "foreground", "f", false, "Run in foreground instead of daemon")
	watchCmd.Flags().BoolVar(&watchDaemonMode, "daemon-mode", false, "")
	watchCmd.Flags().MarkHidden("daemon-mode")

	watchStopCmd.Flags().BoolVar(&watchStopAll, "all", false, "Stop all running daemons")
}

func runWatch(cmd *cobra.Command, args []string) error {
	projectPath, err := resolveProjectPath(args)
	if err != nil {
		return err
	}

	// --daemon-mode: internal flag, run watcher directly (called by daemon.Start)
	if watchDaemonMode {
		return runWatcherForeground(projectPath, true)
	}

	// --foreground: user wants to see output in terminal
	if watchForeground {
		return runWatcherForeground(projectPath, false)
	}

	// Default: start as background daemon
	return runWatchDaemon(projectPath)
}

func runWatchDaemon(projectPath string) error {
	apiClient, err := getClient()
	if err != nil {
		return err
	}

	if err := apiClient.Health(); err != nil {
		return fmt.Errorf("API server not reachable: %w", err)
	}

	_, err = apiClient.GetProject(projectPath)
	if err != nil {
		return fmt.Errorf("project not found — run 'cix init %s' first: %w", projectPath, err)
	}

	pid, err := daemon.Start(projectPath)
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	logFile, _ := daemon.LogFilePath(projectPath)
	fmt.Printf("Watcher started (PID: %d)\n", pid)
	fmt.Printf("Project: %s\n", projectPath)
	fmt.Printf("Logs: %s\n", logFile)

	return nil
}

func runWatcherForeground(projectPath string, silent bool) error {
	apiClient, err := getClient()
	if err != nil {
		return err
	}

	_, err = apiClient.GetProject(projectPath)
	if err != nil {
		return fmt.Errorf("project not found on server — run 'cix init %s' first: %w", projectPath, err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	opts := watcher.Options{
		DebounceMS:  cfg.Watcher.DebounceMS,
		ExcludeDirs: cfg.Watcher.ExcludePatterns,
	}

	w, err := watcher.New(projectPath, apiClient, opts)
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		if !silent {
			fmt.Println("\nShutting down watcher...")
		}
		w.Stop()
	}()

	if !silent {
		fmt.Printf("Watching %s (Ctrl+C to stop)\n", projectPath)
	}

	return w.Start()
}

func runWatchStop(cmd *cobra.Command, args []string) error {
	if watchStopAll {
		stopped, err := daemon.StopAll()
		if err != nil {
			return fmt.Errorf("stop all: %w", err)
		}
		if stopped == 0 {
			fmt.Println("No running daemons")
		} else {
			fmt.Printf("Stopped %d daemon(s)\n", stopped)
		}
		return nil
	}

	projectPath, err := resolveProjectPath(args)
	if err != nil {
		return err
	}

	status := daemon.GetStatus(projectPath)
	if !status.Running {
		fmt.Printf("No watcher running for %s\n", projectPath)
		return nil
	}

	if err := daemon.Stop(projectPath); err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}

	fmt.Printf("Stopped watcher for %s (was PID %d)\n", projectPath, status.PID)
	return nil
}

func runWatchStatus(cmd *cobra.Command, args []string) error {
	projectPath, err := resolveProjectPath(args)
	if err != nil {
		return err
	}

	status := daemon.GetStatus(projectPath)

	if status.Running {
		fmt.Printf("Watcher: running (PID %d)\n", status.PID)
		fmt.Printf("Project: %s\n", status.ProjectPath)
	} else {
		fmt.Printf("Watcher: not running for %s\n", projectPath)
	}

	if status.LogFile != "" {
		fmt.Printf("Log: %s\n", status.LogFile)
	}

	return nil
}

func runWatchList(cmd *cobra.Command, args []string) error {
	all := daemon.ListAll()

	if len(all) == 0 {
		fmt.Println("No running watchers")
		return nil
	}

	fmt.Printf("%d running watcher(s):\n\n", len(all))
	for i, s := range all {
		fmt.Printf("%d. PID %d — %s\n", i+1, s.PID, s.ProjectPath)
		fmt.Printf("   Log: %s\n", s.LogFile)
	}

	return nil
}

func resolveProjectPath(args []string) (string, error) {
	path := "."
	if len(args) > 0 {
		path = args[0]
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return "", fmt.Errorf("directory does not exist: %s", absPath)
	}

	return absPath, nil
}
