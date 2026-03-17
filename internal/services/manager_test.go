package services

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func newTestManager(t *testing.T, cfg *config.Config, dataDir string) *ServiceManager {
	t.Helper()
	return &ServiceManager{
		Config:  cfg,
		DataDir: dataDir,
		Logger:  slog.Default(),
	}
}

func TestList_empty(t *testing.T) {
	cfg := &config.Config{
		Server:   config.Server{ServicesDir: t.TempDir()},
		Services: map[string]config.Service{},
	}
	m := newTestManager(t, cfg, t.TempDir())
	if got := m.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestList_sorted(t *testing.T) {
	cfg := &config.Config{
		Server: config.Server{ServicesDir: t.TempDir()},
		Services: map[string]config.Service{
			"zeppelin": {Path: "stacks/zeppelin", Domain: "z.example.com", UpdateScript: "stacks/zeppelin/update.sh"},
			"alpha":    {Path: "stacks/alpha", Domain: "a.example.com"},
			"beta":     {Path: "stacks/beta", Domain: "b.example.com", AutoDeploy: true},
		},
	}
	m := newTestManager(t, cfg, t.TempDir())
	infos := m.List()

	if len(infos) != 3 {
		t.Fatalf("expected 3 services, got %d", len(infos))
	}
	if infos[0].Name != "alpha" || infos[1].Name != "beta" || infos[2].Name != "zeppelin" {
		t.Fatalf("unexpected order: %v", infos)
	}
	if infos[2].UpdateScript != "stacks/zeppelin/update.sh" {
		t.Fatalf("unexpected UpdateScript: %q", infos[2].UpdateScript)
	}
	if !infos[1].AutoDeploy {
		t.Fatalf("expected beta auto_deploy=true")
	}
}

func TestRunUpdateScript_success(t *testing.T) {
	dataDir := t.TempDir()
	stacksDir := t.TempDir()

	// Create minimal IaC repo structure.
	repoDir := filepath.Join(dataDir, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, "stacks", "mystack"), 0755); err != nil {
		t.Fatal(err)
	}

	// Copy the echo test script into the fake repo.
	scriptSrc := filepath.Join("testdata", "echo.sh")
	scriptDst := filepath.Join(repoDir, "stacks", "mystack", "update.sh")
	data, err := os.ReadFile(scriptSrc)
	if err != nil {
		t.Fatalf("reading testdata/echo.sh: %v", err)
	}
	if err := os.WriteFile(scriptDst, data, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a compose file so findComposeFile succeeds.
	if err := os.WriteFile(filepath.Join(repoDir, "stacks", "mystack", "compose.yaml"), []byte("services:\n  app:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create deploy dir with repo symlink.
	deployDir := filepath.Join(stacksDir, "services", "mystack")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}
	repoLink := filepath.Join(deployDir, "repo")
	if err := os.Symlink(filepath.Join(repoDir, "stacks", "mystack"), repoLink); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{ServicesDir: stacksDir},
		Services: map[string]config.Service{
			"mystack": {
				Path:         "stacks/mystack",
				Domain:       "mystack.example.com",
				UpdateScript: "stacks/mystack/update.sh",
			},
		},
	}

	m := newTestManager(t, cfg, dataDir)
	if err := m.RunUpdateScript(context.Background(), "mystack"); err != nil {
		t.Fatalf("RunUpdateScript failed: %v", err)
	}
}

func TestRunUpdateScript_fail(t *testing.T) {
	dataDir := t.TempDir()
	stacksDir := t.TempDir()

	repoDir := filepath.Join(dataDir, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, "stacks", "mystack"), 0755); err != nil {
		t.Fatal(err)
	}

	scriptSrc := filepath.Join("testdata", "fail.sh")
	scriptDst := filepath.Join(repoDir, "stacks", "mystack", "update.sh")
	data, err := os.ReadFile(scriptSrc)
	if err != nil {
		t.Fatalf("reading testdata/fail.sh: %v", err)
	}
	if err := os.WriteFile(scriptDst, data, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "stacks", "mystack", "compose.yaml"), []byte("services:\n  app:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	deployDir := filepath.Join(stacksDir, "services", "mystack")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoDir, "stacks", "mystack"), filepath.Join(deployDir, "repo")); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{ServicesDir: stacksDir},
		Services: map[string]config.Service{
			"mystack": {
				Path:         "stacks/mystack",
				Domain:       "mystack.example.com",
				UpdateScript: "stacks/mystack/update.sh",
			},
		},
	}

	m := newTestManager(t, cfg, dataDir)
	err = m.RunUpdateScript(context.Background(), "mystack")
	if err == nil {
		t.Fatal("expected error from failing script, got nil")
	}
	if !strings.Contains(err.Error(), "exit code") && !strings.Contains(err.Error(), "exited with code") {
		t.Fatalf("expected exit code in error, got: %v", err)
	}
}

func TestRunUpdateScript_noScript(t *testing.T) {
	cfg := &config.Config{
		Server: config.Server{ServicesDir: t.TempDir()},
		Services: map[string]config.Service{
			"mystack": {Path: "stacks/mystack", Domain: "x.example.com"},
		},
	}
	m := newTestManager(t, cfg, t.TempDir())
	err := m.RunUpdateScript(context.Background(), "mystack")
	if err == nil || !strings.Contains(err.Error(), "no update_script") {
		t.Fatalf("expected 'no update_script' error, got: %v", err)
	}
}

func TestRunUpdateScript_unknownStack(t *testing.T) {
	cfg := &config.Config{
		Server:   config.Server{ServicesDir: t.TempDir()},
		Services: map[string]config.Service{},
	}
	m := newTestManager(t, cfg, t.TempDir())
	err := m.RunUpdateScript(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestStackDeployDir(t *testing.T) {
	cfg := &config.Config{
		Server:   config.Server{ServicesDir: "/opt/deploy"},
		Services: map[string]config.Service{},
	}
	m := newTestManager(t, cfg, "/etc/herald")

	got := m.stackDeployDir("nextcloud")
	want := "/opt/deploy/services/nextcloud"
	if got != want {
		t.Fatalf("stackDeployDir = %q, want %q", got, want)
	}
}

func TestFindComposeFile(t *testing.T) {
	dir := t.TempDir()

	// No compose file.
	if _, err := findComposeFile(dir); err == nil {
		t.Fatal("expected error when no compose file")
	}

	// Create compose.yml.
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := findComposeFile(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "compose.yml" {
		t.Fatalf("got %q, want compose.yml", got)
	}
}

func TestRunUpdateScript_envVars(t *testing.T) {
	dataDir := t.TempDir()
	stacksDir := t.TempDir()

	repoDir := filepath.Join(dataDir, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, "stacks", "mystack"), 0755); err != nil {
		t.Fatal(err)
	}

	// Script that echoes env vars; fails if SERVICE_NAME is wrong.
	script := `#!/bin/bash
set -euo pipefail
[ "$SERVICE_NAME" = "mystack" ] || { echo "bad SERVICE_NAME: $SERVICE_NAME"; exit 1; }
[ -n "$STACK_DIR" ]    || { echo "STACK_DIR empty"; exit 1; }
[ -n "$STACK_DOMAIN" ] || { echo "STACK_DOMAIN empty"; exit 1; }
[ -n "$COMPOSE_FILE" ] || { echo "COMPOSE_FILE empty"; exit 1; }
echo "env ok"
`
	scriptPath := filepath.Join(repoDir, "stacks", "mystack", "update.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "stacks", "mystack", "compose.yaml"), []byte("services:\n  app:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	deployDir := filepath.Join(stacksDir, "services", "mystack")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoDir, "stacks", "mystack"), filepath.Join(deployDir, "repo")); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{ServicesDir: stacksDir},
		Services: map[string]config.Service{
			"mystack": {
				Path:         "stacks/mystack",
				Domain:       "mystack.example.com",
				UpdateScript: "stacks/mystack/update.sh",
			},
		},
	}

	m := newTestManager(t, cfg, dataDir)
	if err := m.RunUpdateScript(context.Background(), "mystack"); err != nil {
		t.Fatalf("RunUpdateScript failed: %v", err)
	}
}
