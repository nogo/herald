package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/nogo/herald/internal/config"
	bootstrap "github.com/nogo/herald/internal/init"
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
	"signal":  true,
	// check-push validates the pushed config.yml, not the deployed one.
	"check-push": true,
	"init":       true,
	// doctor loads and validates the config itself as a check, so it must run even
	// when the config is broken — that is exactly when it is needed.
	"doctor": true,
}

// shouldSkipConfig returns true if the command doesn't need a config file.
func shouldSkipConfig(cmd *cobra.Command) bool {
	// A bare `herald` reports how far setup got, so it must run without a config.
	if !cmd.HasParent() || skipConfigCommands[cmd.Name()] {
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
	Short: "Your repo. Your server. Herald in between.",
	// Usage text buries the error it follows. Wrong arguments and flags get a
	// pointer to --help instead (see usageHint).
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if shouldSkipConfig(cmd) {
			return nil
		}

		// Auto-detect config from <data-dir>/repo/config.yml if --config was not
		// explicitly provided and the default path doesn't exist.
		if !cmd.Flags().Changed("config") {
			autoPath := config.DataDir(dataDir).ConfigFile()
			if _, err := os.Stat(autoPath); err == nil {
				cfgFile = autoPath
			}
		}

		cfg, err := LoadConfigWithToken(cfgFile, dataDir)
		// serve waits for the first push to a bare repo; every other command
		// needs a real config.
		if err != nil && cmd.Name() == "serve" && !cmd.Flags().Changed("config") && errors.Is(err, os.ErrNotExist) {
			if pending, ok := bootstrap.PendingConfig(context.Background(), dataDir); ok {
				cfgFile = config.DataDir(dataDir).ConfigFile()
				cfg, err = pending, nil
			}
		}
		if err != nil {
			if !cmd.Flags().Changed("config") && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)) {
				return notReady(cmd.Context(), cmd.CommandPath())
			}
			return err
		}
		Cfg = cfg
		return nil
	},
}

// notReady explains a missing or unreadable config the way a bare `herald`
// would: where setup stands and the command to run next. A wrong-user hint
// repeats the command the operator typed, so it can be copied as is.
func notReady(ctx context.Context, command string) error {
	s := readSetup(ctx, dataDir, config.DataDir(dataDir).ConfigFile(), func(int) bool { return false })
	hostname, _ := os.Hostname()
	hints := nextStep(s, ownerPrefix(dataDir), nil, hostname)
	headline := command + " needs a set-up server"
	if summary := summarize(s); summary != "" {
		headline += ": " + summary
	}
	if s.stage == stageWrongUser {
		hints[0].cmd += " " + strings.Join(os.Args[1:], " ")
	}
	var b strings.Builder
	b.WriteString(headline)
	for i, h := range hints {
		label := "      "
		if i == 0 {
			label = "next  "
		}
		fmt.Fprintf(&b, "\n  %s%s\n        %s", label, h.cmd, h.why)
	}
	return errors.New(b.String())
}

// usageHint wraps every command's argument check so a wrong number of
// arguments points at that command's --help. Root's own check is left alone:
// cobra already suggests commands for a typo there.
func usageHint(c *cobra.Command) {
	if c.HasParent() && c.Args != nil {
		check := c.Args
		c.Args = func(cmd *cobra.Command, args []string) error {
			if err := check(cmd, args); err != nil {
				return fmt.Errorf("%w\nrun '%s --help' for usage", err, cmd.CommandPath())
			}
			return nil
		}
	}
	for _, sub := range c.Commands() {
		usageHint(sub)
	}
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
			if err := cfg.Server.RequireDeployDomain(); err != nil {
				return nil, err
			}
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
	usageHint(rootCmd)
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\nrun '%s --help' for usage", err, cmd.CommandPath())
	})
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Set here, not in the literal: runHome reads rootCmd's flags, which would
	// make rootCmd's initialisation refer to itself.
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		runHome(cmd.Context(), cmd.OutOrStdout())
		return nil
	}
	rootCmd.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup:"},
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
