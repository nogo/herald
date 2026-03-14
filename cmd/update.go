package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update <stack>",
	Short: "Run update script for a managed stack",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald update: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
