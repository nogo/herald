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

var deployCmd = &cobra.Command{
	Use:   "deploy [app]",
	Short: "Force re-deploy an app",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

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

		store := secrets.NewStore(dataDir)
		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: store,
			Logger:  slog.Default(),
		}

		if err := d.Deploy(context.Background(), appName); err != nil {
			fmt.Fprintf(os.Stderr, "deploy %s: failed: %v\n", appName, err)
			os.Exit(1)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "deploy %s: success\n", appName)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(deployCmd)
}
