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

// --- Round-trip parsing ---

func TestLoad_RepoStackWithTagPattern(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks:
  myapp:
    repo: owner/repo
    branch: main
    tag_pattern: "v*"
    domain: myapp.example.com
    config: config/myapp.yml
    override: |
      services:
        app:
          environment:
            EXTRA: value
    env_file: .env.prod
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.Stacks["myapp"]
	if s.Branch != "main" {
		t.Errorf("branch = %q, want main", s.Branch)
	}
	if s.TagPattern != "v*" {
		t.Errorf("tag_pattern = %q, want v*", s.TagPattern)
	}
	if s.ConfigFile != "config/myapp.yml" {
		t.Errorf("config = %q, want config/myapp.yml", s.ConfigFile)
	}
	if s.EnvFile != ".env.prod" {
		t.Errorf("env_file = %q, want .env.prod", s.EnvFile)
	}
	if !strings.Contains(s.Override, "EXTRA") {
		t.Errorf("override missing expected content: %q", s.Override)
	}
}

func TestLoad_RepoStackWithTag(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks:
  myapp:
    repo: owner/repo
    tag: v1.2.3
    domain: myapp.example.com
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.Stacks["myapp"]
	if s.Tag != "v1.2.3" {
		t.Errorf("tag = %q, want v1.2.3", s.Tag)
	}
	// Tag stacks must not get a branch default.
	if s.Branch != "" {
		t.Errorf("tag stack should have no branch, got %q", s.Branch)
	}
}

func TestLoad_PathStackFullRoundTrip(t *testing.T) {
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
    env_file: .env.svc
    config: config/myservice.yml
    secrets:
      - key: myservice/token
        type: env
        target: API_TOKEN
      - key: myservice/cert
        type: docker-secret
        target: tls_cert
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.Stacks["myservice"]
	if s.Path != "stacks/myservice" {
		t.Errorf("path = %q", s.Path)
	}
	if s.EnvFile != ".env.svc" {
		t.Errorf("env_file = %q", s.EnvFile)
	}
	if s.ConfigFile != "config/myservice.yml" {
		t.Errorf("config = %q", s.ConfigFile)
	}
	if len(s.Secrets) != 2 {
		t.Fatalf("want 2 secrets, got %d", len(s.Secrets))
	}
	if s.Secrets[0].Type != "env" || s.Secrets[0].Target != "API_TOKEN" {
		t.Errorf("secrets[0] = %+v", s.Secrets[0])
	}
	if s.Secrets[1].Type != "docker-secret" || s.Secrets[1].Target != "tls_cert" {
		t.Errorf("secrets[1] = %+v", s.Secrets[1])
	}
}

// --- Edge cases ---

func TestLoad_EmptyStacks(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks: {}
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("empty stacks should be valid: %v", err)
	}
	if len(cfg.Stacks) != 0 {
		t.Errorf("want 0 stacks, got %d", len(cfg.Stacks))
	}
}

func TestLoad_StackNameVariants(t *testing.T) {
	tmp := writeTempConfig(t, `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
stacks:
  my-app:
    repo: owner/repo
    domain: my-app.example.com
  my_service:
    path: stacks/my_service
    domain: my-service.example.com
`)
	cfg, err := config.Load(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Stacks["my-app"]; !ok {
		t.Error("missing stack my-app")
	}
	if _, ok := cfg.Stacks["my_service"]; !ok {
		t.Error("missing stack my_service")
	}
}

// --- Table-driven validation errors ---

func TestLoad_ValidationErrors(t *testing.T) {
	serverBlock := `
server:
  name: test
  deploy_domain: deploy.example.com
  services_dir: /opt/deploy
`
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing deploy_domain",
			yaml: `
server:
  name: test
  services_dir: /opt/deploy
`,
			wantErr: "server.deploy_domain",
		},
		{
			name: "missing services_dir",
			yaml: `
server:
  name: test
  deploy_domain: deploy.example.com
`,
			wantErr: "server.services_dir",
		},
		{
			name: "invalid repo format - no slash",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: invalid-no-slash
    branch: main
    domain: myapp.example.com
`,
			wantErr: "owner/name format",
		},
		{
			name: "invalid repo format - too many slashes",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo/extra
    branch: main
    domain: myapp.example.com
`,
			wantErr: "owner/name format",
		},
		{
			name: "invalid tag_pattern glob",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    tag_pattern: "v[invalid"
    domain: myapp.example.com
`,
			wantErr: "not a valid glob",
		},
		{
			name: "config with absolute path",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    config: /etc/myapp/config.yml
`,
			wantErr: "relative path",
		},
		{
			name: "config with dotdot traversal",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    config: ../escape/config.yml
`,
			wantErr: "relative path",
		},
		{
			name: "length without generate",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: env
        target: MY_SECRET
        length: 32
`,
			wantErr: "length requires generate",
		},
		{
			name: "length below minimum",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: env
        target: MY_SECRET
        generate: base64
        length: 8
`,
			wantErr: "between 16 and 512",
		},
		{
			name: "length above maximum",
			yaml: serverBlock + `
stacks:
  myapp:
    repo: owner/repo
    branch: main
    domain: myapp.example.com
    secrets:
      - key: myapp/secret
        type: env
        target: MY_SECRET
        generate: hex
        length: 1024
`,
			wantErr: "between 16 and 512",
		},
		{
			name: "path stack with tag_pattern",
			yaml: serverBlock + `
stacks:
  svc:
    path: stacks/svc
    domain: svc.example.com
    tag_pattern: "v*"
`,
			wantErr: "tag_pattern",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := writeTempConfig(t, tc.yaml)
			_, err := config.Load(tmp)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			assertContains(t, err.Error(), tc.wantErr)
		})
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
