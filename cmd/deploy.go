package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/ui"
	"github.com/spf13/cobra"
)

var deployAll bool

var deployCmd = &cobra.Command{
	Use:   "deploy [stack]",
	Short: "Deploy a stack",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		store := secrets.NewStore(dataDir)
		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: store,
			Logger:  quietLogger(),
			DataDir: dataDir,
			UI:      ui.NewTTY(os.Stdout),
		}

		if deployAll {
			names := slices.Sorted(maps.Keys(Cfg.Stacks))
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No stacks configured.")
				return nil
			}
			var failed []string
			for _, name := range names {
				d.UI = ui.NewTTY(os.Stdout)
				fmt.Fprintf(cmd.OutOrStdout(), "Deploying %s...\n", name)
				if err := d.Deploy(context.Background(), name, ""); err != nil {
					failed = append(failed, name)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if len(failed) > 0 {
				return fmt.Errorf("%d stack(s) failed to deploy: %s", len(failed), strings.Join(failed, ", "))
			}
			return nil
		}

		if len(args) == 0 {
			names := slices.Sorted(maps.Keys(Cfg.Stacks))
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No stacks configured.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Available stacks:")
			for _, name := range names {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", name)
			}
			return nil
		}

		stackName := args[0]
		if _, ok := Cfg.Stacks[stackName]; !ok {
			names := slices.Sorted(maps.Keys(Cfg.Stacks))
			return fmt.Errorf("stack %q not found. Available: %s", stackName, strings.Join(names, ", "))
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Deploying %s...\n", stackName)
		if err := d.Deploy(context.Background(), stackName, ""); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	deployCmd.GroupID = "stacks"
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().BoolVar(&deployAll, "all", false, "Deploy all configured stacks sequentially")
}
