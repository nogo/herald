package cmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/doctor"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Diagnose why the server is not deploying itself correctly",
	GroupID: "daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ctx := context.Background()

		// doctor runs without the root config preload (see skipConfigCommands) so it
		// can diagnose a broken config. Resolve the path and load it ourselves; a
		// load failure becomes a check, not a fatal error.
		configPath := resolveConfigPath()
		cfg, cfgErr := config.Load(configPath)

		store := secrets.NewStore(dataDir)

		heraldPort := 9483
		token := ""
		if cfg != nil {
			if cfg.Server.Port > 0 {
				heraldPort = cfg.Server.Port
			}
			token = cfg.Server.GithubToken
		}
		if token == "" {
			if t, err := store.Get("herald/github_token"); err == nil {
				token = t
			}
		}

		diag := doctor.Run(ctx, doctor.Deps{
			DataDir:    dataDir,
			Config:     cfg,
			ConfigErr:  cfgErr,
			Secrets:    store,
			Token:      token,
			IaCRepo:    getIaCRepo(dataDir),
			HeraldPort: heraldPort,
			Logger:     quietLogger(),
		})
		diag.Render(cmd.OutOrStdout())
		return nil
	},
}

// resolveConfigPath mirrors the root auto-detect: an explicit --config wins,
// otherwise prefer <data-dir>/repo/config.yml, falling back to the --config default.
func resolveConfigPath() string {
	if rootCmd.PersistentFlags().Changed("config") {
		return cfgFile
	}
	autoPath := filepath.Join(filepath.Clean(dataDir), "repo", "config.yml")
	if _, err := os.Stat(autoPath); err == nil {
		return autoPath
	}
	return cfgFile
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
