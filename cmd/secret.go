package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage encrypted secrets",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var secretSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a secret",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald secret set: not implemented")
	},
}

var secretGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a secret",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald secret get: not implemented")
	},
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secrets",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald secret list: not implemented")
	},
}

var secretImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import secrets",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "herald secret import: not implemented")
	},
}

func init() {
	secretCmd.AddCommand(secretSetCmd, secretGetCmd, secretListCmd, secretImportCmd)
	rootCmd.AddCommand(secretCmd)
}
