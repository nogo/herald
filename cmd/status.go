package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show all services, domains, and health",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald status: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
