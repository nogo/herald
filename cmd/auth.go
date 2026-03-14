package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var authClientID string

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage GitHub authentication",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with GitHub using device flow",
	Long: `Authenticate with GitHub using the OAuth Device Flow.

Opens a URL you paste into any browser (phone, laptop).
The token is stored encrypted in the herald secrets store.

Requires a GitHub OAuth App Client ID. Either:
  1. Set HERALD_GITHUB_CLIENT_ID environment variable, or
  2. Pass --client-id flag

To create a GitHub OAuth App:
  https://github.com/settings/applications/new
  - Application name: Herald
  - Homepage URL: https://github.com/nogo/herald
  - Authorization callback URL: http://localhost
  - Check "Enable Device Flow"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		clientID := authClientID
		if clientID == "" {
			clientID = os.Getenv("HERALD_GITHUB_CLIENT_ID")
		}

		ctx := context.Background()
		token, err := github.DeviceFlowAuth(ctx, os.Stdout, clientID)
		if err != nil {
			return err
		}

		// Store token in secrets
		store := secrets.NewStore(dataDir)
		if err := store.Init(); err != nil {
			return fmt.Errorf("initializing secrets store: %w", err)
		}
		if err := store.Set("herald/github_token", token); err != nil {
			return fmt.Errorf("storing token: %w", err)
		}

		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "  Token stored in herald secrets store.")
		fmt.Fprintln(os.Stdout, "  Herald will use this token for GitHub API calls and git operations.")
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current GitHub authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		store := secrets.NewStore(dataDir)
		token, err := store.Get("herald/github_token")
		if err != nil || token == "" {
			fmt.Fprintln(os.Stdout, "Not authenticated with GitHub.")
			fmt.Fprintln(os.Stdout, "Run: herald auth login")
			return nil
		}

		ctx := context.Background()
		user, err := github.GetUser(ctx, token)
		if err != nil {
			fmt.Fprintln(os.Stdout, "Token stored but may be invalid or expired.")
			fmt.Fprintln(os.Stdout, "Run: herald auth login")
			return nil
		}

		fmt.Fprintf(os.Stdout, "Authenticated as: %s\n", user)
		fmt.Fprintf(os.Stdout, "Token stored in: %s/secrets.age\n", dataDir)
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored GitHub token",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		store := secrets.NewStore(dataDir)
		if err := store.Delete("herald/github_token"); err != nil {
			fmt.Fprintln(os.Stdout, "No GitHub token stored.")
			return nil
		}

		fmt.Fprintln(os.Stdout, "GitHub token removed from secrets store.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)

	authLoginCmd.Flags().StringVar(&authClientID, "client-id", "", "GitHub OAuth App Client ID (or set HERALD_GITHUB_CLIENT_ID)")
}
