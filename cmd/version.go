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
		switch {
		case tag != "" && commit != "unknown":
			fmt.Printf("herald %s (%s) built %s\n", tag, commit, date)
		case tag != "":
			fmt.Printf("herald %s built %s\n", tag, date)
		case commit != "unknown":
			fmt.Printf("herald dev (%s) built %s\n", commit, date)
		default:
			fmt.Printf("herald dev (unknown build)\n")
		}
	},
}

func init() {
	versionCmd.GroupID = "auth"
	rootCmd.AddCommand(versionCmd)
}
