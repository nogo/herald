package cmd

import (
	"context"
	"fmt"
	"os"

	bootstrap "github.com/nogo/herald/internal/init"
	"github.com/spf13/cobra"
)

var (
	initGitHubToken string
	initDataDir     string
	initStacksDir   string
)

var initCmd = &cobra.Command{
	Use:   "init <github-repo>",
	Short: "Bootstrap server from IaC repository",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		serverRepo := args[0]
		if initGitHubToken == "" {
			initGitHubToken = os.Getenv("GITHUB_TOKEN")
		}

		ctx := context.Background()

		if err := bootstrap.CheckPrerequisites(ctx, os.Stdout, initDataDir); err != nil {
			return fmt.Errorf("prerequisite check failed: %w", err)
		}

		opts := bootstrap.Options{
			ServerRepo:  serverRepo,
			GitHubToken: initGitHubToken,
			DataDir:     initDataDir,
			StacksDir:   initStacksDir,
			HeraldPort:  8080,
		}

		if err := bootstrap.Bootstrap(ctx, os.Stdout, opts); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initGitHubToken, "github-token", "", "GitHub personal access token (or set GITHUB_TOKEN)")
	initCmd.Flags().StringVar(&initDataDir, "data-dir", "/etc/herald", "Herald data directory")
	initCmd.Flags().StringVar(&initStacksDir, "stacks-dir", "", "Override stacks directory (default from config)")
}
