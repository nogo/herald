package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/nogo/herald/internal/compose"
	"github.com/spf13/cobra"
)

var (
	logsFollow  bool
	logsTail    string
	logsService string
)

var logsCmd = &cobra.Command{
	Use:   "logs <app|service|preview>",
	Short: "Show container logs",
	Long: `Show logs for an app, service, or preview deployment.

If the compose file defines multiple services, the main service is shown
by default (prefers a service named "app" or matching the app name).
Use -s to target a different service.

Examples:
  herald logs myapp
  herald logs myapp -s budget-migrate
  herald logs myapp --tail 500
  herald logs myapp -f`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		name := args[0]

		cctx, _, err := compose.Resolve(Cfg, dataDir, name)
		if err != nil {
			return err
		}

		service := logsService
		if service == "" {
			svc, _, detectErr := compose.DetectServiceInfo(cctx.ComposeFile, name, "")
			if detectErr == nil {
				service = svc
			}
		}

		dockerArgs := cctx.BaseArgs()
		dockerArgs = append(dockerArgs, "logs")
		if logsFollow {
			dockerArgs = append(dockerArgs, "-f")
		}
		dockerArgs = append(dockerArgs, "--tail", logsTail)
		if service != "" {
			dockerArgs = append(dockerArgs, service)
		}

		c := exec.CommandContext(context.Background(), "docker", dockerArgs...)
		c.Dir = cctx.WorkDir
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		if err := c.Run(); err != nil {
			return fmt.Errorf("docker compose logs: %w", err)
		}
		return nil
	},
}

func init() {
	logsCmd.GroupID = "ops"
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	logsCmd.Flags().StringVar(&logsTail, "tail", "100", "Number of lines to show from the end")
	logsCmd.Flags().StringVarP(&logsService, "service", "s", "", "Target service name (default: auto-detect main service)")
}
