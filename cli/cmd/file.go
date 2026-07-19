package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dvcdsys/code-index/cli/internal/client"
	"github.com/spf13/cobra"
)

var (
	fileProject string
	fileName    string
	fileLines   string
	fileRaw     bool
)

// fileCmd represents the file command.
var fileCmd = &cobra.Command{
	Use:   "file <path>",
	Short: "Read a file (whole or a line range) from an external project",
	Long: `Read a file from the server-side checkout of an EXTERNAL (GitHub-backed)
project — useful for navigating workspace / external repos the harness cannot
see locally.

Only works for external projects (the server keeps their files on disk). For a
LOCAL project, read the file directly with your own tools (it lives on your
machine) — the server returns an error pointing you there.

Examples:
  cix file internal/httpapi/server.go -n github.com/owner/repo@main
  cix file README.md --lines 1:40 -n github.com/owner/repo@main
  cix file main.go --lines 120 -n github.com/owner/repo@main   # single line`,
	Args: cobra.ExactArgs(1),
	RunE: runFile,
}

func init() {
	rootCmd.AddCommand(fileCmd)
	fileCmd.Flags().StringVarP(&fileProject, "project", "p", "", "Project path (default: current directory)")
	fileCmd.Flags().StringVarP(&fileName, "name", "n", "", "Project ID (exact match against `cix list`). Mutually exclusive with -p.")
	fileCmd.Flags().StringVar(&fileLines, "lines", "", "Line range N:M (1-based, inclusive). Also accepts N, N:, :M.")
	fileCmd.Flags().BoolVar(&fileRaw, "raw", false, "Print raw content without line numbers")
	fileCmd.MarkFlagsMutuallyExclusive("project", "name")
}

func runFile(cmd *cobra.Command, args []string) error {
	file := args[0]

	start, end, err := parseLineRange(fileLines)
	if err != nil {
		return err
	}

	apiClient, err := getClient()
	if err != nil {
		return err
	}

	absPath, err := resolveProjectArg(fileProject, fileName, apiClient)
	if err != nil {
		return err
	}

	result, err := apiClient.ReadFile(absPath, file, start, end)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}

	// No lines in range (empty file, or a range past EOF): the server signals
	// this with end_line < start_line. Decide on those authoritative fields, not
	// on Content == "" (a single blank line is legitimately empty content).
	if result.EndLine < result.StartLine {
		return nil
	}

	if fileRaw {
		fmt.Print(result.Content)
		if !strings.HasSuffix(result.Content, "\n") {
			fmt.Println()
		}
	} else {
		lines := strings.Split(result.Content, "\n")
		width := len(strconv.Itoa(result.StartLine + len(lines)))
		for i, line := range lines {
			fmt.Printf("%*d  %s\n", width, result.StartLine+i, line)
		}
	}
	// Truncation warning goes to stderr in both modes, so it never pollutes a
	// piped raw copy while still telling the user the file was cut short.
	if result.Truncated {
		fmt.Fprintf(os.Stderr, "\n(truncated — file has %d lines; showing %d–%d)\n",
			result.TotalLines, result.StartLine, result.EndLine)
	}
	return nil
}

// parseLineRange parses "N:M", "N:", ":M", or "N" into (start, end). Empty
// string → (0, 0) meaning whole file. 0 means "unbounded" on that side.
func parseLineRange(s string) (start, end int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	if !strings.Contains(s, ":") {
		n, perr := strconv.Atoi(s)
		if perr != nil || n < 1 {
			return 0, 0, fmt.Errorf("invalid --lines %q: want N, N:M, N:, or :M", s)
		}
		return n, n, nil
	}
	parts := strings.SplitN(s, ":", 2)
	if parts[0] != "" {
		if start, err = strconv.Atoi(parts[0]); err != nil || start < 1 {
			return 0, 0, fmt.Errorf("invalid --lines start in %q", s)
		}
	}
	if parts[1] != "" {
		if end, err = strconv.Atoi(parts[1]); err != nil || end < 1 {
			return 0, 0, fmt.Errorf("invalid --lines end in %q", s)
		}
	}
	if start > 0 && end > 0 && end < start {
		return 0, 0, fmt.Errorf("invalid --lines %q: end < start", s)
	}
	return start, end, nil
}

// resolveProjectArg resolves the -p/-n/cwd selection to a project identity the
// client can address, mirroring the resolution used by search/summary/files.
func resolveProjectArg(projectFlag, nameFlag string, apiClient *client.Client) (string, error) {
	if nameFlag != "" {
		return resolveProjectByName(nameFlag, apiClient)
	}
	projectPath := projectFlag
	if projectPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
		projectPath = cwd
	}
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return findProjectRoot(abs, apiClient), nil
}
