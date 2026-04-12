package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestPortFromAny(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"3000", "3000"},
		{"3000:3000", "3000"},
		{"0.0.0.0:80:3000", "3000"},
		{3000, "3000"},
		{8080, "8080"},
		{map[string]any{"target": 3000, "published": 3000}, "3000"},
		{map[string]any{"target": "8080"}, "8080"},
		{nil, ""},
	}
	for _, tc := range tests {
		got := portFromAny(tc.in)
		if got != tc.want {
			t.Errorf("portFromAny(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDetectServiceInfo(t *testing.T) {
	t.Run("service named app", func(t *testing.T) {
		content := `
services:
  app:
    expose:
      - "3000"
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "myapp", "3000")
		if name != "app" {
			t.Errorf("service name = %q, want %q", name, "app")
		}
		if port != "3000" {
			t.Errorf("port = %q, want %q", port, "3000")
		}
	})

	t.Run("service matches app name", func(t *testing.T) {
		content := `
services:
  budget:
    ports:
      - "8080:8080"
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "budget", "3000")
		if name != "budget" {
			t.Errorf("service name = %q, want %q", name, "budget")
		}
		if port != "8080" {
			t.Errorf("port = %q, want %q", port, "8080")
		}
	})

	t.Run("first service fallback", func(t *testing.T) {
		content := `
services:
  web:
    expose:
      - 4000
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "other", "3000")
		if name != "db" && name != "web" {
			t.Errorf("service name = %q, want one of [db web]", name)
		}
		_ = port
	})

	t.Run("port from long form", func(t *testing.T) {
		content := `
services:
  app:
    ports:
      - target: 5000
        published: 80
`
		_, port := writeComposeAndDetect(t, content, "app", "3000")
		if port != "5000" {
			t.Errorf("port = %q, want %q", port, "5000")
		}
	})

	t.Run("no port defaults to defaultPort", func(t *testing.T) {
		content := `
services:
  app:
    image: myapp
`
		_, port := writeComposeAndDetect(t, content, "app", "3000")
		if port != "3000" {
			t.Errorf("port = %q, want %q", port, "3000")
		}
	})

	t.Run("stack default port 80", func(t *testing.T) {
		content := `
services:
  app:
    image: myapp
`
		_, port := writeComposeAndDetect(t, content, "app", "80")
		if port != "80" {
			t.Errorf("port = %q, want %q", port, "80")
		}
	})
}

func writeComposeAndDetect(t *testing.T, content, appName, defaultPort string) (name, port string) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	name, port, err := DetectServiceInfo(f, appName, defaultPort)
	if err != nil {
		t.Fatalf("DetectServiceInfo: %v", err)
	}
	return name, port
}

func TestDeepMerge(t *testing.T) {
	t.Run("overlay wins on conflict", func(t *testing.T) {
		base := map[string]any{"a": "base", "b": "keep"}
		overlay := map[string]any{"a": "overlay"}
		got := DeepMerge(base, overlay)
		if got["a"] != "overlay" {
			t.Errorf("a = %v, want overlay", got["a"])
		}
		if got["b"] != "keep" {
			t.Errorf("b = %v, want keep", got["b"])
		}
	})

	t.Run("nested maps merged", func(t *testing.T) {
		base := map[string]any{
			"services": map[string]any{
				"app": map[string]any{"image": "old"},
			},
		}
		overlay := map[string]any{
			"services": map[string]any{
				"app": map[string]any{"restart": "always"},
			},
		}
		got := DeepMerge(base, overlay)
		svc := got["services"].(map[string]any)["app"].(map[string]any)
		if svc["image"] != "old" {
			t.Errorf("image = %v, want old", svc["image"])
		}
		if svc["restart"] != "always" {
			t.Errorf("restart = %v, want always", svc["restart"])
		}
	})

	t.Run("overlay adds new keys", func(t *testing.T) {
		base := map[string]any{"a": 1}
		overlay := map[string]any{"b": 2}
		got := DeepMerge(base, overlay)
		if got["a"] != 1 || got["b"] != 2 {
			t.Errorf("got %v, want {a:1, b:2}", got)
		}
	})
}

func TestDeepMergeYAML(t *testing.T) {
	t.Run("preserves override tag", func(t *testing.T) {
		base := `
services:
  budget-app:
    env_file: !override
      - /opt/deploy/apps/budget/.env
    labels:
      caddy: budget.example.com
    networks:
      - caddy
networks:
  caddy:
    external: true
`
		overlay := `
services:
  budget-app:
    env_file: !override
      - /opt/deploy/apps/budget/.env
  budget-migrate:
    env_file: !override
      - /opt/deploy/apps/budget/.env
volumes:
  budget-data:
    driver: local
`
		merged, err := DeepMergeYAML([]byte(base), []byte(overlay))
		if err != nil {
			t.Fatalf("DeepMergeYAML: %v", err)
		}
		result := string(merged)

		// The !override tag must be preserved in the merged output.
		if !strings.Contains(result, "!override") {
			t.Errorf("merged YAML lost !override tag:\n%s", result)
		}

		// Both services should be present.
		if !strings.Contains(result, "budget-app") {
			t.Errorf("merged YAML missing budget-app:\n%s", result)
		}
		if !strings.Contains(result, "budget-migrate") {
			t.Errorf("merged YAML missing budget-migrate:\n%s", result)
		}

		// Volumes from overlay should be present.
		if !strings.Contains(result, "budget-data") {
			t.Errorf("merged YAML missing budget-data volume:\n%s", result)
		}

		// Labels from base should survive the merge.
		if !strings.Contains(result, "budget.example.com") {
			t.Errorf("merged YAML lost caddy label from base:\n%s", result)
		}

		// Networks from base should survive.
		if !strings.Contains(result, "caddy") {
			t.Errorf("merged YAML lost caddy network:\n%s", result)
		}
	})

	t.Run("overlay replaces non-map values", func(t *testing.T) {
		base := `
services:
  app:
    image: old:v1
    labels:
      caddy: old.example.com
`
		overlay := `
services:
  app:
    image: new:v2
`
		merged, err := DeepMergeYAML([]byte(base), []byte(overlay))
		if err != nil {
			t.Fatalf("DeepMergeYAML: %v", err)
		}
		result := string(merged)

		if !strings.Contains(result, "new:v2") {
			t.Errorf("overlay image not applied:\n%s", result)
		}
		// Labels from base should survive (maps merge).
		if !strings.Contains(result, "old.example.com") {
			t.Errorf("base labels lost:\n%s", result)
		}
	})
}

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	envVars := map[string]string{
		"DB_URL":  "postgres://localhost/db",
		"API_KEY": "secret123",
	}

	if err := WriteEnvFile(root, envVars); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Keys should be sorted: API_KEY before DB_URL
	if content != "API_KEY=secret123\nDB_URL=postgres://localhost/db\n" {
		t.Errorf("unexpected .env content:\n%s", content)
	}
}

func TestWriteEnvFileEmpty(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := WriteEnvFile(root, map[string]string{}); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %q", string(data))
	}
}

func TestResolveStack(t *testing.T) {
	t.Run("repo stack paths", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			Server: config.Server{ServicesDir: dir},
			Stacks: map[string]config.Stack{
				"myapp": {
					Repo:    "owner/myapp",
					Branch:  "main",
					Compose: "compose.yml",
					Domain:  "myapp.example.com",
				},
			},
		}

		ctx, err := ResolveStack(cfg, "myapp")
		if err != nil {
			t.Fatal(err)
		}
		if ctx.ProjectName != "herald-myapp" {
			t.Errorf("ProjectName = %q, want %q", ctx.ProjectName, "herald-myapp")
		}
		wantDeployDir := filepath.Join(dir, "myapp")
		wantWorkDir := filepath.Join(wantDeployDir, "repo")
		if ctx.WorkDir != wantWorkDir {
			t.Errorf("WorkDir = %q, want %q", ctx.WorkDir, wantWorkDir)
		}
		wantCompose := filepath.Join(wantDeployDir, "repo", "compose.yml")
		if ctx.ComposeFile != wantCompose {
			t.Errorf("ComposeFile = %q, want %q", ctx.ComposeFile, wantCompose)
		}
	})

	t.Run("path stack finds compose file", func(t *testing.T) {
		dir := t.TempDir()
		// Set up the symlinked repo dir with a compose file.
		repoDir := filepath.Join(dir, "myservice", "repo")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoDir, "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := &config.Config{
			Server: config.Server{ServicesDir: dir},
			Stacks: map[string]config.Stack{
				"myservice": {
					Path:   "svc/myservice",
					Domain: "myservice.example.com",
				},
			},
		}

		ctx, err := ResolveStack(cfg, "myservice")
		if err != nil {
			t.Fatal(err)
		}
		if ctx.ProjectName != "herald-myservice" {
			t.Errorf("ProjectName = %q, want %q", ctx.ProjectName, "herald-myservice")
		}
		wantCompose := filepath.Join(repoDir, "compose.yaml")
		if ctx.ComposeFile != wantCompose {
			t.Errorf("ComposeFile = %q, want %q", ctx.ComposeFile, wantCompose)
		}
	})

	t.Run("existing override and env files are resolved", func(t *testing.T) {
		dir := t.TempDir()
		deployDir := filepath.Join(dir, "myapp")
		repoDir := filepath.Join(deployDir, "repo")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create override and env files so they're picked up.
		os.WriteFile(filepath.Join(deployDir, "compose.override.yml"), []byte("{}"), 0644)
		os.WriteFile(filepath.Join(deployDir, ".env"), []byte("K=v\n"), 0644)

		cfg := &config.Config{
			Server: config.Server{ServicesDir: dir},
			Stacks: map[string]config.Stack{
				"myapp": {
					Repo:    "owner/myapp",
					Branch:  "main",
					Compose: "compose.yml",
					Domain:  "myapp.example.com",
				},
			},
		}

		ctx, err := ResolveStack(cfg, "myapp")
		if err != nil {
			t.Fatal(err)
		}
		if ctx.OverrideFile == "" {
			t.Error("expected OverrideFile to be set")
		}
		if ctx.EnvFile == "" {
			t.Error("expected EnvFile to be set")
		}
	})

	t.Run("not found", func(t *testing.T) {
		cfg := &config.Config{
			Server: config.Server{ServicesDir: t.TempDir()},
			Stacks: map[string]config.Stack{},
		}
		_, err := ResolveStack(cfg, "missing")
		if err == nil {
			t.Fatal("expected error for missing stack")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
