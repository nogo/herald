package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/status"
	"github.com/spf13/cobra"
)

var webhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage GitHub webhooks",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var webhooksSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Register missing webhooks and remove stale ones",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if Cfg.Server.GithubToken == "" {
			return fmt.Errorf("GitHub token not configured. Run 'herald auth login' or set GITHUB_TOKEN environment variable")
		}

		store := secrets.NewStore(dataDir)
		client := github.NewGitHubClient(Cfg.Server.GithubToken, slog.Default())

		force, _ := cmd.Flags().GetBool("force")
		iacRepo := getIaCRepo(dataDir)
		results, err := github.SyncWebhooks(context.Background(), Cfg, store, client, force, iacRepo)
		if err != nil {
			return err
		}

		// Save webhook state for use by herald status.
		ws := &status.WebhookState{
			SyncedAt: time.Now().UTC(),
			Repos:    make(map[string]status.WebhookEntry),
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Webhooks synced:")
		for _, r := range results {
			suffix := ""
			if iacRepo != "" && r.Repo == iacRepo {
				suffix = " (IaC)"
			}
			switch r.Action {
			case "exists":
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s ✓ exists (id: %d)%s\n", r.Repo, r.ID, suffix)
				ws.Repos[r.Repo] = status.WebhookEntry{ID: r.ID, Registered: true}
			case "created":
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s + created (id: %d)%s\n", r.Repo, r.ID, suffix)
				ws.Repos[r.Repo] = status.WebhookEntry{ID: r.ID, Registered: true}
			case "error":
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s ! error: %v%s\n", r.Repo, r.Error, suffix)
			}
		}
		wsPath := status.WebhookStatePath(dataDir)
		if err := status.SaveWebhookState(wsPath, ws); err != nil {
			fmt.Fprintf(os.Stderr, "warning: saving webhook state: %v\n", err)
		}

		return nil
	},
}

var webhooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List webhook status for all repos in config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if Cfg.Server.GithubToken == "" {
			return fmt.Errorf("GitHub token not configured. Run 'herald auth login' or set GITHUB_TOKEN environment variable")
		}

		client := github.NewGitHubClient(Cfg.Server.GithubToken, slog.Default())
		iacRepo := getIaCRepo(dataDir)
		statuses := github.ListWebhookStatuses(context.Background(), Cfg, client, iacRepo)

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "Repo\tWebhook URL\tStatus\tKind")
		hasErrors := false
		for _, s := range statuses {
			kind := "stack"
			if iacRepo != "" && s.Repo == iacRepo {
				kind = "IaC"
			}
			switch {
			case s.Error != nil:
				hasErrors = true
				fmt.Fprintf(w, "%s\t%s\terror: %v\t%s\n", s.Repo, "-", s.Error, kind)
			case s.Found:
				state := "active"
				if !s.Active {
					state = "inactive"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Repo, s.URL, state, kind)
			default:
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Repo, "-", "not registered", kind)
			}
		}
		w.Flush()

		if hasErrors {
			return fmt.Errorf("one or more repos could not be queried")
		}
		return nil
	},
}

func init() {
	webhooksSyncCmd.Flags().Bool("force", false, "Delete and recreate all webhooks (use after changing webhook secret)")
	webhooksCmd.AddCommand(webhooksSyncCmd, webhooksListCmd)
	webhooksCmd.GroupID = "infra"
	rootCmd.AddCommand(webhooksCmd)
}
