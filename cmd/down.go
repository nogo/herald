package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var downVolumes bool

var downCmd = &cobra.Command{
	Use:   "down <app>",
	Short: "Stop and remove an app's containers",
	Long: `Stop and remove the containers for an app.

The deploy directory (repo, .env, secrets) is preserved.
Run 'herald deploy <app>' to bring it back up.

Use --volumes to also remove named Docker volumes (databases, uploads).
This is irreversible without a backup.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		appName := args[0]
		if _, ok := Cfg.Apps[appName]; !ok {
			return fmt.Errorf("app %q not found in config", appName)
		}

		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: secrets.NewStore(dataDir),
			Logger:  slog.Default(),
			DataDir: dataDir,
		}

		if err := d.Down(context.Background(), appName, downVolumes); err != nil {
			return fmt.Errorf("down %s: %w", appName, err)
		}

		fmt.Printf("down %s: containers stopped\n", appName)
		return nil
	},
}

func init() {
	downCmd.GroupID = "apps"
	rootCmd.AddCommand(downCmd)
	downCmd.Flags().BoolVar(&downVolumes, "volumes", false, "Also remove named volumes (irreversible without a backup)")
}
