package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile config with running state",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald sync: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
