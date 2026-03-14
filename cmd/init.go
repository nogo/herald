package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init <repo>",
	Short: "Bootstrap server from IaC repository",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald init: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
