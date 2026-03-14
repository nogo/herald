package cmd

import (
	"fmt"
	"os"
	"os/user"

	"github.com/nogo/herald/internal/systemd"
	"github.com/spf13/cobra"
)

var (
	installUser   string
	installStart  bool
	installEnable bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install herald as a systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getuid() != 0 {
			fmt.Fprintln(os.Stderr, "Installing systemd service requires root. Run: sudo herald install")
			os.Exit(1)
		}

		binaryPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("detect binary path: %w", err)
		}

		if installUser == "" {
			cu, err := user.Current()
			if err != nil {
				return fmt.Errorf("detect current user: %w", err)
			}
			installUser = cu.Username
		}

		u, err := user.Lookup(installUser)
		if err != nil {
			fmt.Fprintf(os.Stderr, "User '%s' does not exist. Create it first: sudo useradd -r -s /bin/false -G docker herald\n", installUser)
			os.Exit(1)
		}

		g, err := user.LookupGroupId(u.Gid)
		if err != nil {
			return fmt.Errorf("lookup primary group: %w", err)
		}

		warnDockerGroup(u, installUser)

		stacksDir := "/opt/deploy"
		if Cfg != nil && Cfg.Server.StacksDir != "" {
			stacksDir = Cfg.Server.StacksDir
		}

		cfg := systemd.ServiceConfig{
			BinaryPath: binaryPath,
			ConfigPath: cfgFile,
			DataDir:    dataDir,
			User:       installUser,
			Group:      g.Name,
			StacksDir:  stacksDir,
		}

		if err := systemd.Install(cfg); err != nil {
			return err
		}

		status := "stopped"
		if installEnable {
			if err := systemd.Enable(); err != nil {
				return err
			}
		}
		if installStart {
			if err := systemd.Start(); err != nil {
				return err
			}
			status = "running"
		}

		enabledStr := "disabled"
		if installEnable {
			enabledStr = "enabled"
		}

		fmt.Printf(`Herald service installed:
  Unit:    /etc/systemd/system/herald.service
  User:    %s
  Config:  %s
  Data:    %s
  Env:     /etc/herald/environment
  Status:  %s (%s)

Commands:
  systemctl status herald     View service status
  journalctl -u herald -f     Follow logs
  systemctl restart herald    Restart service
  herald uninstall            Remove service
`, installUser, cfgFile, dataDir, status, enabledStr)

		return nil
	},
}

func warnDockerGroup(u *user.User, username string) {
	gids, err := u.GroupIds()
	if err != nil {
		return
	}
	for _, gid := range gids {
		g, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}
		if g.Name == "docker" {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Warning: user '%s' is not in the 'docker' group. Herald needs Docker access.\n", username)
}

func init() {
	rootCmd.AddCommand(installCmd)
	installCmd.Flags().StringVar(&installUser, "user", "", "User to run the service as (default: current user)")
	installCmd.Flags().BoolVar(&installStart, "start", true, "Start the service immediately after installing")
	installCmd.Flags().BoolVar(&installEnable, "enable", true, "Enable the service to start on boot")
}
