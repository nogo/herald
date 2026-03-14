package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start webhook listener and deploy daemon",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald serve: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
