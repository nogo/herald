package cmd

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	verbose bool
	dataDir string

	// Cfg holds the loaded config, accessible to all subcommands.
	Cfg *config.Config
)

// skipConfigCommands are commands that run without a config file.
var skipConfigCommands = map[string]bool{
	"version": true,
	"init":    true,
	// doctor loads and validates the config itself as a check, so it must run even
	// when the config is broken — that is exactly when it is needed.
	"doctor": true,
}

// shouldSkipConfig returns true if the command doesn't need a config file.
func shouldSkipConfig(cmd *cobra.Command) bool {
	if skipConfigCommands[cmd.Name()] {
		return true
	}
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "auth" || p.Name() == "secret" {
			return true
		}
	}
	return false
}

var rootCmd = &cobra.Command{
	Use:   "herald",
	Short: "herald — deploy daemon for self-hosted infrastructure",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if shouldSkipConfig(cmd) {
			return nil
		}

		// Auto-detect config from <data-dir>/repo/config.yml if --config was not
		// explicitly provided and the default path doesn't exist.
		if !cmd.Flags().Changed("config") {
			autoPath := filepath.Join(filepath.Clean(dataDir), "repo", "config.yml")
			if _, err := os.Stat(autoPath); err == nil {
				cfgFile = autoPath
			}
		}

		cfg, err := LoadConfigWithToken(cfgFile, dataDir)
		if err != nil {
			return err
		}
		Cfg = cfg
		return nil
	},
}

// LoadConfigWithToken loads the config file and applies the secrets store
// token fallback. Use this instead of config.Load directly to ensure the
// GitHub token from herald auth login is always available.
func LoadConfigWithToken(cfgPath, dDir string) (*config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if cfg.Server.GithubToken == "" {
		store := secrets.NewStore(dDir)
		if token, err := store.Get("herald/github_token"); err == nil && token != "" {
			cfg.Server.GithubToken = token
		}
	}
	return cfg, nil
}

// quietLogger returns a logger that only emits WARN and above.
// Used in CLI commands where the UI handles progress output.
func quietLogger() *slog.Logger {
	if verbose {
		return slog.Default()
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddGroup(
		&cobra.Group{ID: "daemon", Title: "Daemon:"},
		&cobra.Group{ID: "stacks", Title: "Stacks:"},
		&cobra.Group{ID: "previews", Title: "Previews:"},
		&cobra.Group{ID: "secrets", Title: "Secrets:"},
		&cobra.Group{ID: "infra", Title: "Infrastructure:"},
		&cobra.Group{ID: "auth", Title: "Auth:"},
	)
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "/etc/herald/config.yml", "Path to config file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "/etc/herald", "Directory for age key and secrets file")
}
