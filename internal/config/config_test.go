package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

// writeTempConfig writes yaml to a temp file and returns its path.
func writeTempConfig(t *testing.T, yaml string) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "config*.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tmp.WriteString(yaml)
	tmp.Close()
	return tmp.Name()
}

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

	if len(cfg.Stacks) != 4 {
		t.Fatalf("want 4 stacks, got %d", len(cfg.Stacks))
	}

	budget := cfg.Stacks["budget"]
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

	nextcloud := cfg.Stacks["nextcloud"]
	if nextcloud.Path != "stacks/nextcloud" {
		t.Errorf("nextcloud.path = %q", nextcloud.Path)
	}
	if nextcloud.UpdateScript != "stacks/nextcloud/update.sh" {
		t.Errorf("nextcloud.update = %q", nextcloud.UpdateScript)
	}

	// Verify computed backwards-compat fields.
	if len(cfg.Apps) != 2 {
		t.Fatalf("want 2 apps in computed field, got %d", len(cfg.Apps))
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("want 2 services in computed field, got %d", len(cfg.Services))
	}
	if _, ok := cfg.Apps["budget"]; !ok {
		t.Error("cfg.Apps missing budget")
	}
	if _, ok := cfg.Services["nextcloud"]; !ok {
		t.Error("cfg.Services missing nextcloud")
	}
}

func TestLoad_DefaultBranchAndCompose(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks:
  myapp:
    repo: owner/repo
    domain: myapp.example.com
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stack := cfg.Stacks["myapp"]
	if stack.Branch != "main" {
		t.Errorf("default branch = %q, want main", stack.Branch)
	}
	if stack.Compose != "compose.yml" {
		t.Errorf("default compose = %q, want compose.yml", stack.Compose)
	}
	// Also accessible via computed Apps field.
	if cfg.Apps["myapp"].Branch != "main" {
		t.Errorf("cfg.Apps default branch = %q, want main", cfg.Apps["myapp"].Branch)
	}
}

func TestLoad_PathStack(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks:
  myservice:
    path: stacks/myservice
    domain: myservice.example.com
    auto_deploy: true
    update: stacks/myservice/update.sh
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stack := cfg.Stacks["myservice"]
	if stack.Path != "stacks/myservice" {
		t.Errorf("path = %q", stack.Path)
	}
	if !stack.AutoDeploy {
		t.Error("auto_deploy should be true")
	}
	if stack.UpdateScript != "stacks/myservice/update.sh" {
		t.Errorf("update = %q", stack.UpdateScript)
	}
	// Path stacks get no branch/compose defaults.
	if stack.Branch != "" {
		t.Errorf("path stack should have no branch, got %q", stack.Branch)
	}
	if stack.Compose != "" {
		t.Errorf("path stack should have no compose, got %q", stack.Compose)
	}
	if _, ok := cfg.Services["myservice"]; !ok {
		t.Error("cfg.Services missing myservice")
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 9483 {
		t.Errorf("default port = %d, want 9483", cfg.Server.Port)
	}
	if cfg.Server.AcmeEmail != "webmaster@deploy.example.com" {
		t.Errorf("default acme_email = %q", cfg.Server.AcmeEmail)
	}
}

func TestLoad_MissingServerName(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  deploy_domain: deploy.example.com
  services_dir: /opt
`)
	_, err := config.Load(tmp)
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

func TestLoad_MissingSource(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    domain: myapp.example.com
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for stack with no repo or path")
	}
	assertContains(t, err.Error(), "one of repo or path is required")
}

func TestLoad_BothRepoAndPath(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    path: stacks/myapp
    domain: myapp.example.com
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for stack with both repo and path")
	}
	assertContains(t, err.Error(), "mutually exclusive")
}

func TestLoad_InvalidSecretType(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: invalid-type
        target: MY_SECRET
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "invalid-type")
}

func TestLoad_PreviewEnabledWithoutDomain(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    preview:
      enabled: true
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "preview.domain")
}

func TestLoad_PreviewDomainWithoutWildcard(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    preview:
      enabled: true
      domain: preview.example.com
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	assertContains(t, err.Error(), "wildcard")
}

func TestLoad_PathStackWithBranch(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  svc:
    path: stacks/svc
    domain: svc.example.com
    branch: main
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for path stack with branch")
	}
	assertContains(t, err.Error(), "branch")
}

func TestLoad_PathStackWithTag(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  svc:
    path: stacks/svc
    domain: svc.example.com
    tag: v1.0.0
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for path stack with tag")
	}
	assertContains(t, err.Error(), "tag")
}

func TestLoad_PathStackWithPreview(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  svc:
    path: stacks/svc
    domain: svc.example.com
    preview:
      enabled: false
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for path stack with preview")
	}
	assertContains(t, err.Error(), "preview")
}

func TestLoad_PathStackWithCompose(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  svc:
    path: stacks/svc
    domain: svc.example.com
    compose: compose.yml
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for path stack with compose")
	}
	assertContains(t, err.Error(), "compose")
}

func TestLoad_TagAndBranchExclusive(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    branch: main
    tag: v1.0.0
    domain: myapp.example.com
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for tag+branch set")
	}
	assertContains(t, err.Error(), "mutually exclusive")
}

func TestLoad_TagPatternRequiresBranch(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    tag: v1.0.0
    tag_pattern: "v*"
    domain: myapp.example.com
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for tag_pattern with tag")
	}
	assertContains(t, err.Error(), "tag_pattern")
}

func TestLoad_GenerateValidation(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: env
        target: MY_SECRET
        generate: invalid-algo
`)
	_, err := config.Load(tmp)
	if err == nil {
		t.Fatal("expected error for invalid generate value")
	}
	assertContains(t, err.Error(), "generate")
}

func TestLoad_GithubTokenEnvVar(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_test123")

	cfg, err := config.Load("testdata/valid.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.GithubToken != "ghp_test123" {
		t.Errorf("github_token not expanded: got %q", cfg.Server.GithubToken)
	}
}

func TestLoad_GithubTokenUnsetWarning(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	cfg, err := config.Load("testdata/valid.yml")
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
