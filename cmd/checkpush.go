package cmd

import (
	"os"

	bootstrap "github.com/nogo/herald/internal/init"
	"github.com/spf13/cobra"
)

// checkPushCmd is what the bare repo's pre-receive hook runs; operators never
// call it directly.
var checkPushCmd = &cobra.Command{
	Use:    "check-push",
	Short:  "Reject a push to the bare server repo that the daemon would refuse",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return bootstrap.CheckPush(cmd.Context(), dataDir, os.Stdin)
	},
}

func init() {
	rootCmd.AddCommand(checkPushCmd)
}
