package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var previewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Manage preview deployments",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald preview: not implemented")
	},
}

var previewListCmd = &cobra.Command{
	Use:   "list",
	Short: "List preview deployments",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald preview list: not implemented")
	},
}

func init() {
	previewCmd.AddCommand(previewListCmd)
	rootCmd.AddCommand(previewCmd)
}
