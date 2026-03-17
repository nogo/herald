package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nogo/herald/internal/github"
	bootstrap "github.com/nogo/herald/internal/init"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var (
	initGitHubToken string
	initClientID    string
	initServicesDir string
)

var initCmd = &cobra.Command{
	Use:   "init <github-repo>",
	Short: "Bootstrap server from IaC repository",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		serverRepo := args[0]
		ctx := context.Background()

		// Resolve GitHub token: flag > env > secrets store > device flow
		token, err := resolveGitHubToken(ctx, initGitHubToken, initClientID, dataDir)
		if err != nil {
			return err
		}

		if err := bootstrap.CheckPrerequisites(ctx, os.Stdout, dataDir); err != nil {
			return fmt.Errorf("prerequisite check failed: %w", err)
		}

		opts := bootstrap.Options{
			ServerRepo:  serverRepo,
			GitHubToken: token,
			DataDir:     dataDir,
			ServicesDir: initServicesDir,
			HeraldPort:  0, // resolved from config in Bootstrap
		}

		if err := bootstrap.Bootstrap(ctx, os.Stdout, opts); err != nil {
			return err
		}

		return nil
	},
}

// resolveGitHubToken tries multiple sources for a GitHub token:
//  1. --github-token flag
//  2. GITHUB_TOKEN env var
//  3. Secrets store (from a previous herald auth login)
//  4. OAuth Device Flow (interactive)
func resolveGitHubToken(ctx context.Context, flagToken, clientID, dDir string) (string, error) {
	// 1. Explicit flag
	if flagToken != "" {
		return flagToken, nil
	}

	// 2. Environment variable
	if env := os.Getenv("GITHUB_TOKEN"); env != "" {
		return env, nil
	}

	// 3. Secrets store (from previous auth login)
	store := secrets.NewStore(dDir)
	if stored, err := store.Get("herald/github_token"); err == nil && stored != "" {
		user, err := github.GetUser(ctx, stored)
		if err == nil {
			fmt.Fprintf(os.Stdout, "Using stored GitHub token (authenticated as %s)\n", user)
			return stored, nil
		}
		fmt.Fprintln(os.Stdout, "Stored GitHub token is invalid or expired.")
	}

	// 4. Device flow
	if clientID == "" {
		clientID = os.Getenv("HERALD_GITHUB_CLIENT_ID")
	}
	if clientID != "" {
		fmt.Fprintln(os.Stdout, "No GitHub token found. Starting device flow authentication...")
		token, err := github.DeviceFlowAuth(ctx, os.Stdout, clientID)
		if err != nil {
			return "", fmt.Errorf("device flow auth: %w", err)
		}
		if err := store.Init(); err == nil {
			_ = store.Set("herald/github_token", token)
		}
		return token, nil
	}

	return "", fmt.Errorf("GitHub token required. Provide one of:\n" +
		"  --github-token <token>\n" +
		"  GITHUB_TOKEN environment variable\n" +
		"  herald auth login (interactive device flow)")
}

func init() {
	initCmd.GroupID = "auth"
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initGitHubToken, "github-token", "", "GitHub personal access token (or set GITHUB_TOKEN)")
	initCmd.Flags().StringVar(&initClientID, "client-id", "", "GitHub OAuth App Client ID for device flow (or set HERALD_GITHUB_CLIENT_ID)")
	initCmd.Flags().StringVar(&initServicesDir, "services-dir", "", "Override services directory (default from config)")
}
