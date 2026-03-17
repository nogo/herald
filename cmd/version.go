package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	commit = "unknown"
	tag    = ""
	date   = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print herald version",
	Run: func(cmd *cobra.Command, args []string) {
		if tag != "" {
			fmt.Printf("herald %s (%s) built %s\n", tag, commit, date)
		} else {
			fmt.Printf("herald (%s) built %s\n", commit, date)
		}
	},
}

func init() {
	versionCmd.GroupID = "auth"
	rootCmd.AddCommand(versionCmd)
}
