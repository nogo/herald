package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/nogo/herald/internal/compose"
	"github.com/spf13/cobra"
)

var execService string

var execCmd = &cobra.Command{
	Use:   "exec <app|service|preview> [-- command]",
	Short: "Execute a command in a running container",
	Long: `Execute a command in a running app, service, or preview container.

If the compose file defines multiple services, the main service is targeted
by default (prefers a service named "app" or matching the app name).
Use -s to target a different service.

The default command is "sh". Use -- to pass a different command.

Examples:
  herald exec myapp
  herald exec myapp -s budget-migrate
  herald exec myapp -- /bin/bash
  herald exec myapp -s budget-migrate -- cat /etc/hosts`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		name := args[0]
		userCmd := args[1:] // everything after -- ends up here
		if len(userCmd) == 0 {
			userCmd = []string{"sh"}
		}

		cctx, _, err := compose.Resolve(Cfg, dataDir, name)
		if err != nil {
			return err
		}

		service := execService
		if service == "" {
			svc, _, detectErr := compose.DetectServiceInfo(cctx.ComposeFile, name, "")
			if detectErr != nil {
				return fmt.Errorf("could not detect service (use -s to specify): %w", detectErr)
			}
			service = svc
		}

		dockerArgs := cctx.BaseArgs()
		dockerArgs = append(dockerArgs, "exec", "-it", service)
		dockerArgs = append(dockerArgs, userCmd...)

		c := exec.CommandContext(context.Background(), "docker", dockerArgs...)
		c.Dir = cctx.WorkDir
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		if err := c.Run(); err != nil {
			return fmt.Errorf("docker compose exec: %w", err)
		}
		return nil
	},
}

func init() {
	execCmd.GroupID = "ops"
	rootCmd.AddCommand(execCmd)
	execCmd.Flags().StringVarP(&execService, "service", "s", "", "Target service name (default: auto-detect main service)")
}
