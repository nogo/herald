package cmd

import (
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/config"
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
	"version":   true,
	"init":      true,
	"install":   true,
	"uninstall": true,
}

var rootCmd = &cobra.Command{
	Use:   "herald",
	Short: "herald — deploy daemon for self-hosted infrastructure",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if skipConfigCommands[cmd.Name()] {
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

		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}
		Cfg = cfg
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "/etc/herald/config.yml", "Path to config file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "/etc/herald/", "Directory for age key and secrets file")
}
