package systemd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const UnitPath = "/etc/systemd/system/herald.service"
const EnvironmentFilePath = "/etc/herald/environment"

const environmentFileContent = `# Herald environment variables
# Add environment variables here. They will be loaded by the systemd service.
# Example:
# GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx
`

// ServiceConfig holds the values needed to generate the systemd unit file.
type ServiceConfig struct {
	BinaryPath  string
	ConfigPath  string
	DataDir     string
	User        string
	Group       string
	HomeDir     string
	ServicesDir string
}

var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=Herald - deployment daemon
Documentation=https://github.com/nogo/herald
After=network-online.target docker.service
Wants=network-online.target docker.service
Requires=docker.service

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
ExecStart={{.BinaryPath}} serve --config {{.ConfigPath}} --data-dir {{.DataDir}}
Restart=on-failure
RestartSec=10
TimeoutStartSec=30
TimeoutStopSec=30

# Environment — secrets go in the environment file, never in the unit file
EnvironmentFile=-{{.DataDir}}/environment

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths={{.DataDir}} {{.ServicesDir}} /var/run/docker.sock {{.HomeDir}}
PrivateTmp=true
ProtectHome=read-only
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectHostname=true
ProtectClock=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=true
SystemCallArchitectures=native
# Note: docker socket access is root-equivalent; these directives harden the
# herald process surface but do not contain a compromised docker socket.

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=herald

[Install]
WantedBy=multi-user.target
`))

// GenerateUnitFile returns the content of the systemd unit file for the given config.
func GenerateUnitFile(cfg ServiceConfig) string {
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	var buf bytes.Buffer
	if err := unitTemplate.Execute(&buf, cfg); err != nil {
		panic(fmt.Sprintf("systemd: unit template error: %v", err))
	}
	return buf.String()
}

// Install writes the unit file and environment file, then runs daemon-reload.
func Install(cfg ServiceConfig) error {
	unit := GenerateUnitFile(cfg)
	if err := os.WriteFile(UnitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := ensureEnvironmentFile(cfg.DataDir, cfg.User); err != nil {
		return err
	}

	if err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	return nil
}

// ensureEnvironmentFile creates the environment file if it doesn't already exist.
func ensureEnvironmentFile(dataDir, user string) error {
	envPath := filepath.Join(filepath.Clean(dataDir), "environment")
	if _, err := os.Stat(envPath); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(envPath, []byte(environmentFileContent), 0600); err != nil {
		return fmt.Errorf("write environment file: %w", err)
	}
	return nil
}

// Enable enables the herald service to start on boot.
func Enable() error {
	return systemctl("enable", "herald")
}

// Start starts the herald service.
func Start() error {
	return systemctl("start", "herald")
}

// Uninstall stops, disables, and removes the herald service unit file.
func Uninstall() error {
	// Stop (ignore error if not running).
	_ = systemctl("stop", "herald")

	// Disable (ignore error if not enabled).
	_ = systemctl("disable", "herald")

	if err := os.Remove(UnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	if err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	return nil
}

// IsInstalled reports whether the herald service unit file exists.
func IsInstalled() bool {
	_, err := os.Stat(UnitPath)
	return err == nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w\n%s", args, err, out)
	}
	return nil
}
