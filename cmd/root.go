package cmd

import (
	"os"

	"github.com/nogo/herald/internal/config"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	verbose bool

	// Cfg holds the loaded config, accessible to all subcommands.
	Cfg *config.Config
)

// skipConfigCommands are commands that run without a config file.
var skipConfigCommands = map[string]bool{
	"version": true,
}

var rootCmd = &cobra.Command{
	Use:   "herald",
	Short: "herald — deploy daemon for self-hosted infrastructure",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if skipConfigCommands[cmd.Name()] {
			return nil
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
}
