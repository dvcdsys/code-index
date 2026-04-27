package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print cix CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("cix %s %s/%s\n", Version, runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.Flags().BoolP("version", "v", false, "Print version and exit")
}
