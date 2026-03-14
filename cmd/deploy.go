package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy <app>",
	Short: "Force re-deploy an app",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald deploy: not implemented")
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
}
