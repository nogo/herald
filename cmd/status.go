package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/status"
	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show all services, domains, and health",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		caddyMgr := &caddy.CaddyManager{
			Config:     Cfg,
			Logger:     slog.Default(),
			HeraldPort: port,
		}

		previewMgr := &preview.PreviewManager{
			Config:  Cfg,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		collector := &status.StatusCollector{
			Config:  Cfg,
			DataDir: dataDir,
			Logger:  slog.Default(),
			Caddy:   caddyMgr,
			Preview: previewMgr,
		}

		ctx := context.Background()
		s, err := collector.Collect(ctx)
		if err != nil {
			return err
		}

		if statusJSON {
			data, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		}

		printStatus(cmd.OutOrStdout(), s)
		return nil
	},
}

func stateIcon(state string) string {
	switch state {
	case "running":
		return "● running"
	case "degraded":
		return "◐ degraded"
	case "stopped":
		return "○ stopped"
	case "not deployed":
		return "○ not deployed"
	case "error":
		return "✗ error"
	default:
		return "? " + state
	}
}

func printStatus(w io.Writer, s *status.ServerStatus) {
	title := fmt.Sprintf("Herald Status — %s", s.ServerName)
	sep := strings.Repeat("═", len([]rune(title))+4)
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, sep)
	fmt.Fprintln(w)

	// Caddy.
	caddyLine := "○ stopped"
	if s.Caddy.Running {
		caddyLine = "● running"
		if s.Caddy.Uptime != "" {
			caddyLine += " (up " + s.Caddy.Uptime + ")"
		}
	}
	fmt.Fprintf(w, "Caddy: %s\n", caddyLine)
	if s.Caddy.Email != "" {
		fmt.Fprintf(w, "  ACME: %s\n", s.Caddy.Email)
	}
	if s.Caddy.DomainCount > 0 {
		fmt.Fprintf(w, "  Domains: %d active\n", s.Caddy.DomainCount)
	}

	// Apps.
	if len(s.Apps) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Apps:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Name\tDomain\tStatus\tContainers\tRef")
		for _, a := range s.Apps {
			ref := a.DeployedRef
			if strings.HasPrefix(ref, "refs/tags/") {
				ref = "tag:" + strings.TrimPrefix(ref, "refs/tags/")
			}
			if ref == "" {
				ref = "-"
			}
			containers := ""
			if a.State != "not deployed" {
				containers = fmt.Sprintf("%d/%d", a.ContainersUp, a.ContainersTotal)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
				a.Name, a.Domain, stateIcon(a.State), containers, ref)
		}
		tw.Flush()
	}

	// Services.
	if len(s.Stacks) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Services:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Name\tDomain\tStatus\tContainers")
		for _, st := range s.Stacks {
			containers := fmt.Sprintf("%d/%d", st.ContainersUp, st.ContainersTotal)
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				st.Name, st.Domain, stateIcon(st.State), containers)
		}
		tw.Flush()
	}

	// Previews.
	if len(s.Previews) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Previews:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  App\tDomain\tBranch\tAge")
		for _, p := range s.Previews {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.AppName, p.Domain, p.Branch, p.Age)
		}
		tw.Flush()
	}

	// Webhooks.
	if len(s.Webhooks) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Webhooks:")
		if !s.WebhookSyncedAt.IsZero() {
			fmt.Fprintf(w, "  (last synced: %s)\n", s.WebhookSyncedAt.Local().Format(time.RFC3339))
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Repo\tStatus")
		for _, wh := range s.Webhooks {
			var whStatus string
			switch {
			case wh.Unknown:
				whStatus = "? unknown"
			case wh.Registered:
				whStatus = "● registered"
			default:
				whStatus = "✗ not registered"
			}
			fmt.Fprintf(tw, "  %s\t%s\n", wh.Repo, whStatus)
		}
		tw.Flush()
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output as JSON")
}
