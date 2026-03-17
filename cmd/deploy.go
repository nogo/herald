package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var deployAll bool

var deployCmd = &cobra.Command{
	Use:   "deploy [app]",
	Short: "Force re-deploy an app",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		store := secrets.NewStore(dataDir)
		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: store,
			Logger:  slog.Default(),
			DataDir: dataDir,
		}

		if deployAll {
			names := slices.Sorted(maps.Keys(Cfg.Apps))
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No apps configured.")
				return nil
			}
			var failed []string
			for _, name := range names {
				fmt.Fprintf(cmd.OutOrStdout(), "deploying %s...\n", name)
				if err := d.Deploy(context.Background(), name, ""); err != nil {
					fmt.Fprintf(os.Stderr, "deploy %s: failed: %v\n", name, err)
					failed = append(failed, name)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "deploy %s: success\n", name)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("%d app(s) failed to deploy: %s", len(failed), strings.Join(failed, ", "))
			}
			return nil
		}

		if len(args) == 0 {
			names := slices.Sorted(maps.Keys(Cfg.Apps))
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No apps configured.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Available apps:")
			for _, name := range names {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", name)
			}
			return nil
		}

		appName := args[0]
		if _, ok := Cfg.Apps[appName]; !ok {
			names := slices.Sorted(maps.Keys(Cfg.Apps))
			return fmt.Errorf("app %q not found. Available: %s", appName, strings.Join(names, ", "))
		}

		if err := d.Deploy(context.Background(), appName, ""); err != nil {
			return fmt.Errorf("deploy %s: %w", appName, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "deploy %s: success\n", appName)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().BoolVar(&deployAll, "all", false, "Deploy all configured apps sequentially")
}
