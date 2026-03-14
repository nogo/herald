package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var previewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Manage preview deployments",
	Long: `Manage preview deployments.

Preview deployments are created automatically when a feature branch is pushed
or a pull request is opened for an app with preview.enabled: true.

DNS requirement: add a wildcard record pointing to this server's IP, e.g.
  *.preview.example.com → <server-ip>`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help() //nolint:errcheck
	},
}

var previewListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active preview deployments",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		mgr := newPreviewManager()
		previews, err := mgr.List()
		if err != nil {
			return err
		}
		if len(previews) == 0 {
			fmt.Println("No active previews.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tDomain\tApp\tBranch\tCreated")
		for _, p := range previews {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				p.ID, p.Domain, p.AppName, p.Branch, formatAge(time.Since(p.CreatedAt)))
		}
		return w.Flush()
	},
}

var previewRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a preview deployment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		mgr := newPreviewManager()
		if err := mgr.Remove(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Printf("preview '%s' removed\n", args[0])
		return nil
	},
}

var previewCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove previews for branches that no longer exist on the remote",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return newPreviewManager().Cleanup(cmd.Context())
	},
}

func newPreviewManager() *preview.PreviewManager {
	return &preview.PreviewManager{
		Config:  Cfg,
		Secrets: secrets.NewStore(dataDir),
		DataDir: dataDir,
		Logger:  slog.Default(),
	}
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func init() {
	previewCmd.AddCommand(previewListCmd)
	previewCmd.AddCommand(previewRemoveCmd)
	previewCmd.AddCommand(previewCleanupCmd)
	rootCmd.AddCommand(previewCmd)
}
