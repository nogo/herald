package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestLoad_Valid(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_test123")

	cfg, err := config.Load("testdata/valid.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Name != "srv2" {
		t.Errorf("server.name = %q, want %q", cfg.Server.Name, "srv2")
	}
	if cfg.Server.DeployDomain != "deploy.example.com" {
		t.Errorf("server.deploy_domain = %q", cfg.Server.DeployDomain)
	}
	if cfg.Server.GithubToken != "ghp_test123" {
		t.Errorf("github_token not expanded: got %q", cfg.Server.GithubToken)
	}

	if len(cfg.Apps) != 2 {
		t.Fatalf("want 2 apps, got %d", len(cfg.Apps))
	}

	budget := cfg.Apps["budget"]
	if budget.Repo != "nogo/budget-app" {
		t.Errorf("budget.repo = %q", budget.Repo)
	}
	if budget.Compose != "compose.yml" {
		t.Errorf("budget.compose = %q, want compose.yml", budget.Compose)
	}
	if len(budget.Secrets) != 1 || budget.Secrets[0].Type != "env" {
		t.Errorf("budget secrets: %+v", budget.Secrets)
	}
	if budget.Preview == nil {
		t.Error("budget.preview is nil")
	}

	if len(cfg.Stacks) != 2 {
		t.Fatalf("want 2 stacks, got %d", len(cfg.Stacks))
	}
}

func TestLoad_DefaultBranchAndCompose(t *testing.T) {
	// Create a minimal config with no branch or compose set.
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(`
server:
  name: test
  deploy_domain: deploy.example.com
  stacks_dir: /opt/deploy
apps:
  myapp:
    repo: owner/repo
    domain: myapp.example.com
`)
	tmp.Close()

	cfg, err := config.Load(tmp.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	app := cfg.Apps["myapp"]
	if app.Branch != "main" {
		t.Errorf("default branch = %q, want main", app.Branch)
	}
	if app.Compose != "compose.yml" {
		t.Errorf("default compose = %q, want compose.yml", app.Compose)
	}
}

func TestLoad_MissingServerName(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(`
server:
  deploy_domain: deploy.example.com
  stacks_dir: /opt
`)
	tmp.Close()

	_, err = config.Load(tmp.Name())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "server.name")
}

func TestLoad_MissingDomain(t *testing.T) {
	_, err := config.Load("testdata/invalid_missing_domain.yml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "domain is required")
	assertContains(t, err.Error(), "budget")
}

func TestLoad_DuplicateDomain(t *testing.T) {
	_, err := config.Load("testdata/invalid_duplicate_domain.yml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "shared.example.com")
}

func TestLoad_InvalidSecretType(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(`
server:
  name: test
  deploy_domain: deploy.example.com
  stacks_dir: /opt
apps:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: invalid-type
        target: MY_SECRET
`)
	tmp.Close()

	_, err = config.Load(tmp.Name())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "invalid-type")
}

func TestLoad_PreviewEnabledWithoutDomain(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(`
server:
  name: test
  deploy_domain: deploy.example.com
  stacks_dir: /opt
apps:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    preview:
      enabled: true
`)
	tmp.Close()

	_, err = config.Load(tmp.Name())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "preview.domain")
}

func TestLoad_PreviewDomainWithoutWildcard(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(`
server:
  name: test
  deploy_domain: deploy.example.com
  stacks_dir: /opt
apps:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    preview:
      enabled: true
      domain: preview.example.com
`)
	tmp.Close()

	_, err = config.Load(tmp.Name())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "wildcard")
}

func TestLoad_GithubTokenUnsetWarning(t *testing.T) {
	// Ensure GITHUB_TOKEN is not set.
	t.Setenv("GITHUB_TOKEN", "")

	cfg, err := config.Load("testdata/valid.yml")
	// Should still load (empty token is not a hard error).
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.GithubToken != "" {
		t.Errorf("expected empty token, got %q", cfg.Server.GithubToken)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load("testdata/nonexistent.yml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
