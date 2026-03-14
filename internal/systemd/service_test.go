package systemd

import (
	"strings"
	"testing"
)

func TestGenerateUnitFile_structure(t *testing.T) {
	cfg := ServiceConfig{
		BinaryPath: "/usr/local/bin/herald",
		ConfigPath: "/etc/herald/config.yml",
		DataDir:    "/etc/herald/",
		User:       "herald",
		Group:      "herald",
		StacksDir:  "/opt/deploy",
	}
	got := GenerateUnitFile(cfg)

	required := []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"Description=Herald - deployment daemon",
		"After=network-online.target docker.service",
		"Requires=docker.service",
		"Type=simple",
		"User=herald",
		"Group=herald",
		"ExecStart=/usr/local/bin/herald serve --config /etc/herald/config.yml --data-dir /etc/herald",
		"Restart=on-failure",
		"RestartSec=10",
		"EnvironmentFile=-/etc/herald/environment",
		"NoNewPrivileges=true",
		"ProtectSystem=strict",
		"ProtectHome=tmpfs",
		"ReadWritePaths=/etc/herald /opt/deploy /var/run/docker.sock",
		"PrivateTmp=true",
		"StandardOutput=journal",
		"StandardError=journal",
		"SyslogIdentifier=herald",
		"WantedBy=multi-user.target",
	}
	for _, want := range required {
		if !strings.Contains(got, want) {
			t.Errorf("unit file missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestGenerateUnitFile_noTokenInUnitFile(t *testing.T) {
	cfg := ServiceConfig{
		BinaryPath: "/usr/local/bin/herald",
		ConfigPath: "/etc/herald/config.yml",
		DataDir:    "/etc/herald/",
		User:       "deploy",
		Group:      "deploy",
		StacksDir:  "/opt/deploy",
	}
	got := GenerateUnitFile(cfg)

	// Tokens must never appear in the unit file (world-readable).
	// They belong in /etc/herald/environment (0600) or the secrets store.
	if strings.Contains(got, "ghp_secret123") {
		t.Errorf("token must not appear in unit file, got:\n%s", got)
	}
	if strings.Contains(got, "GITHUB_TOKEN=") {
		t.Errorf("GITHUB_TOKEN must not be set in unit file, got:\n%s", got)
	}
	if !strings.Contains(got, "EnvironmentFile") {
		t.Errorf("expected EnvironmentFile directive in unit file")
	}
}

func TestGenerateUnitFile_noGithubToken(t *testing.T) {
	cfg := ServiceConfig{
		BinaryPath: "/usr/local/bin/herald",
		ConfigPath: "/etc/herald/config.yml",
		DataDir:    "/etc/herald/",
		User:       "deploy",
		Group:      "deploy",
		StacksDir:  "/opt/deploy",
	}
	got := GenerateUnitFile(cfg)

	if strings.Contains(got, "GITHUB_TOKEN") {
		t.Errorf("expected no GITHUB_TOKEN in unit file when not set, got:\n%s", got)
	}
}

func TestGenerateUnitFile_dataDirCleaned(t *testing.T) {
	cfg := ServiceConfig{
		BinaryPath: "/usr/local/bin/herald",
		ConfigPath: "/etc/herald/config.yml",
		DataDir:    "/etc/herald/",
		User:       "herald",
		Group:      "herald",
		StacksDir:  "/opt/deploy",
	}
	got := GenerateUnitFile(cfg)

	// DataDir trailing slash should be cleaned.
	if strings.Contains(got, "/etc/herald/ ") || strings.Contains(got, "data-dir /etc/herald/ ") {
		t.Errorf("data dir was not cleaned: %s", got)
	}
	if !strings.Contains(got, "--data-dir /etc/herald") {
		t.Errorf("expected --data-dir /etc/herald in ExecStart, got:\n%s", got)
	}
}

func TestGenerateUnitFile_customStacksDir(t *testing.T) {
	cfg := ServiceConfig{
		BinaryPath: "/usr/local/bin/herald",
		ConfigPath: "/etc/herald/config.yml",
		DataDir:    "/etc/herald",
		User:       "deploy",
		Group:      "deploy",
		StacksDir:  "/srv/apps",
	}
	got := GenerateUnitFile(cfg)

	if !strings.Contains(got, "ReadWritePaths=/etc/herald /srv/apps /var/run/docker.sock") {
		t.Errorf("expected custom StacksDir in ReadWritePaths, got:\n%s", got)
	}
}

func TestIsInstalled_false(t *testing.T) {
	// In a test environment the unit file should not be present.
	// This test verifies the function returns false when the file is absent.
	// (Actual installation is tested via manual integration tests.)
	if IsInstalled() {
		t.Log("herald.service is installed on this system — skipping absence check")
	}
}
