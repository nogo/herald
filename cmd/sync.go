package cmd

import (
	"context"
	"os"
	"sync/atomic"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/maintenance"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/ui"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile config with running state",
	Long: `Run a maintenance pass: pull the IaC repo, reload and validate config,
ensure Caddy is running, reconcile GitHub webhooks (create, repair, and prune
stale hooks), survey stacks, redeploy changed auto_deploy path stacks, and report
drift. This is the same pass the daemon runs on startup and on IaC pushes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ctx := context.Background()

		store := secrets.NewStore(dataDir)
		live := &atomic.Pointer[config.Config]{}
		live.Store(Cfg)

		d := &deployer.Deployer{
			Config:     Cfg,
			LiveConfig: live,
			Secrets:    store,
			Logger:     quietLogger(),
			DataDir:    dataDir,
			UI:         ui.NewTTY(os.Stdout),
		}

		runner := &maintenance.Runner{
			DataDir:    dataDir,
			Logger:     quietLogger(),
			Secrets:    store,
			Deployer:   d,
			Live:       live,
			Reload:     func() (*config.Config, error) { return LoadConfigWithToken(cfgFile, dataDir) },
			IaCRepo:    getIaCRepo(dataDir),
			HeraldPort: effectivePort(),
		}

		rep := runner.Run(ctx, maintenance.Options{
			Pull:            true,
			Webhooks:        maintenance.ReconcileFull,
			RedeployChanged: true,
			BlockOnDeploys:  true,
		})
		rep.Render(cmd.OutOrStdout())
		return nil
	},
}

func init() {
	syncCmd.GroupID = "daemon"
	rootCmd.AddCommand(syncCmd)
}
