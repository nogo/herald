package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull latest server config from the IaC repo",
	Long: `Pulls the latest changes from the server's IaC repository.

This updates config.yml, stack compose files, and deploy scripts.
Running services are not affected until you restart herald or
run herald sync.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		repoDir := filepath.Join(dataDir, "repo")
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
			return fmt.Errorf("no IaC repo at %s — run 'herald init' first", repoDir)
		}

		fmt.Fprintln(os.Stdout, "Pulling IaC repo...")
		pullCmd := exec.Command("git", "-C", repoDir, "pull", "--ff-only")
		out, err := pullCmd.CombinedOutput()
		output := strings.TrimSpace(string(out))

		if err != nil {
			return fmt.Errorf("git pull failed: %s", output)
		}

		if output == "Already up to date." {
			fmt.Fprintln(os.Stdout, "Already up to date.")
		} else {
			fmt.Fprintln(os.Stdout, output)
			fmt.Fprintln(os.Stdout, "\nConfig updated. Restart herald to apply: sudo systemctl restart herald")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(pullCmd)
}
