package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"

	githelper "github.com/nogo/herald/internal/git"
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

		token := resolveToken()

		fmt.Fprintln(os.Stdout, "Pulling IaC repo...")
		output, err := githelper.PullFFOnly(token, repoDir)

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

// resolveToken gets the GitHub token from config or secrets store.
func resolveToken() string {
	if Cfg != nil && Cfg.Server.GithubToken != "" {
		return Cfg.Server.GithubToken
	}
	store := secrets.NewStore(dataDir)
	if token, err := store.Get("herald/github_token"); err == nil && token != "" {
		return token
	}
	return ""
}

func init() {
	rootCmd.AddCommand(pullCmd)
}
