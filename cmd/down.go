package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/ui"
	"github.com/spf13/cobra"
)

var downVolumes bool

var downCmd = &cobra.Command{
	Use:   "down <stack>",
	Short: "Stop and remove a stack's containers",
	Long: `Stop and remove the containers for a stack.

The deploy directory (repo, .env, secrets) is preserved.
Run 'herald deploy <stack>' to bring it back up.

Use --volumes to also remove named Docker volumes (databases, uploads).
This is irreversible without a backup.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		stackName := args[0]
		if _, ok := Cfg.Stacks[stackName]; !ok {
			return fmt.Errorf("stack %q not found in config", stackName)
		}

		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: secrets.NewStore(dataDir),
			Logger:  quietLogger(),
			DataDir: dataDir,
			UI:      ui.NewTTY(os.Stdout),
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Stopping %s...\n", stackName)
		if err := d.Down(context.Background(), stackName, downVolumes); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	downCmd.GroupID = "stacks"
	rootCmd.AddCommand(downCmd)
	downCmd.Flags().BoolVar(&downVolumes, "volumes", false, "Also remove named volumes (irreversible without a backup)")
}
