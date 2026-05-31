package cmd

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"text/tabwriter"

	"github.com/anthropics/code-index/cli/internal/config"
	"github.com/anthropics/code-index/cli/internal/config/schema"
	"github.com/spf13/cobra"
)

var configKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "List every settable configuration key",
	Long: `Print every configuration key the CLI knows about, with its
current value, default, env-var override (if any), and a short description.

The list is reflection-driven — any new schema-tagged field shows up here
automatically. Slice keys managed via dedicated commands (servers,
projects) are not listed; use 'cix config show' to view them.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return renderConfigKeys(os.Stdout, cfg)
	},
}

func init() {
	configCmd.AddCommand(configKeysCmd)
}

func renderConfigKeys(w io.Writer, cfg *config.Config) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "KEY\tVALUE\tDEFAULT\tENV\tDESCRIPTION"); err != nil {
		return err
	}

	if err := schema.Walk(cfg, func(l schema.LeafField) {
		// Skip slice-of-struct leaves (servers, projects); they have
		// purpose-built management commands and don't render meaningfully
		// in a single tab-separated row.
		if l.Value.Kind() == reflect.Slice && l.Value.Type().Elem().Kind() == reflect.Struct {
			return
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			l.Path,
			formatLeafCurrent(l),
			dashIfEmpty(l.Tag("default")),
			dashIfEmpty(l.Tag("env")),
			l.Tag("desc"),
		)
	}); err != nil {
		return err
	}
	return tw.Flush()
}

// formatLeafCurrent returns the leaf's current value as a display string.
// Sensitive leaves render only "(set)"/"(not set)" — same gate as
// renderScalarLeaf in `cix config show`.
func formatLeafCurrent(l schema.LeafField) string {
	if l.Sensitive() {
		// Tag-gated: do not bind the value to a named *Key/*Secret var.
		// reflect.Value.IsZero() inspects through reflection only.
		if l.Value.IsZero() {
			return "(not set)"
		}
		return "(set)"
	}
	return fmt.Sprintf("%v", l.Value.Interface())
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
