package cmd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

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
		cmd.SilenceUsage = true
		if os.Getuid() != 0 {
			return fmt.Errorf("installing systemd service requires root. Run: sudo herald install")
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
			return fmt.Errorf("user %q does not exist. Create it first: sudo useradd -r -s /bin/false -G docker herald", installUser)
		}

		g, err := user.LookupGroupId(u.Gid)
		if err != nil {
			return fmt.Errorf("lookup primary group: %w", err)
		}

		warnDockerGroup(u, installUser)

		// Resolve config path: use auto-detected repo config if --config wasn't explicit
		configPath := cfgFile
		if !cmd.Flags().Changed("config") {
			autoPath := filepath.Join(filepath.Clean(dataDir), "repo", "config.yml")
			if _, err := os.Stat(autoPath); err == nil {
				configPath = autoPath
			}
		}

		stacksDir := "/opt/deploy"
		// Try loading config for services_dir (optional — install works without config)
		if loadedCfg, err := LoadConfigWithToken(configPath, dataDir); err == nil && loadedCfg.Server.ServicesDir != "" {
			stacksDir = loadedCfg.Server.ServicesDir
		}

		cfg := systemd.ServiceConfig{
			BinaryPath: binaryPath,
			ConfigPath: configPath,
			DataDir:    dataDir,
			User:       installUser,
			Group:      g.Name,
			ServicesDir: stacksDir,
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
