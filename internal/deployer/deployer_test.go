package deployer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestSymlinkSource(t *testing.T) {
	t.Run("creates symlink", func(t *testing.T) {
		dir := t.TempDir()
		iacDir := filepath.Join(dir, "iac")
		target := filepath.Join(iacDir, "repo", "mystack")
		if err := os.MkdirAll(target, 0755); err != nil {
			t.Fatal(err)
		}

		d := &Deployer{DataDir: iacDir, Logger: discardLogger()}
		deployDir := filepath.Join(dir, "deploy", "mystack")
		if err := os.MkdirAll(deployDir, 0755); err != nil {
			t.Fatal(err)
		}

		stack := config.Stack{Path: "mystack"}
		if err := d.symlinkSource(deployDir, stack); err != nil {
			t.Fatal(err)
		}

		repoLink := filepath.Join(deployDir, "repo")
		got, err := os.Readlink(repoLink)
		if err != nil {
			t.Fatalf("symlink not created: %v", err)
		}
		if got != target {
			t.Errorf("symlink target = %q, want %q", got, target)
		}
	})

	t.Run("idempotent - same target", func(t *testing.T) {
		dir := t.TempDir()
		iacDir := filepath.Join(dir, "iac")
		target := filepath.Join(iacDir, "repo", "mystack")
		if err := os.MkdirAll(target, 0755); err != nil {
			t.Fatal(err)
		}

		d := &Deployer{DataDir: iacDir, Logger: discardLogger()}
		deployDir := filepath.Join(dir, "deploy", "mystack")
		if err := os.MkdirAll(deployDir, 0755); err != nil {
			t.Fatal(err)
		}

		stack := config.Stack{Path: "mystack"}
		if err := d.symlinkSource(deployDir, stack); err != nil {
			t.Fatal(err)
		}
		// Second call must not error.
		if err := d.symlinkSource(deployDir, stack); err != nil {
			t.Fatalf("second call should be idempotent: %v", err)
		}
	})

	t.Run("stale symlink is recreated", func(t *testing.T) {
		dir := t.TempDir()
		iacDir := filepath.Join(dir, "iac")
		oldTarget := filepath.Join(iacDir, "repo", "oldstack")
		newTarget := filepath.Join(iacDir, "repo", "newstack")
		if err := os.MkdirAll(oldTarget, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(newTarget, 0755); err != nil {
			t.Fatal(err)
		}

		d := &Deployer{DataDir: iacDir, Logger: discardLogger()}
		deployDir := filepath.Join(dir, "deploy", "mystack")
		if err := os.MkdirAll(deployDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create stale symlink pointing to oldTarget.
		repoLink := filepath.Join(deployDir, "repo")
		if err := os.Symlink(oldTarget, repoLink); err != nil {
			t.Fatal(err)
		}

		stack := config.Stack{Path: "newstack"}
		if err := d.symlinkSource(deployDir, stack); err != nil {
			t.Fatal(err)
		}

		got, err := os.Readlink(repoLink)
		if err != nil {
			t.Fatal(err)
		}
		if got != newTarget {
			t.Errorf("symlink target = %q, want %q", got, newTarget)
		}
	})

	t.Run("missing path returns error", func(t *testing.T) {
		dir := t.TempDir()
		iacDir := filepath.Join(dir, "iac")
		if err := os.MkdirAll(filepath.Join(iacDir, "repo"), 0755); err != nil {
			t.Fatal(err)
		}

		d := &Deployer{DataDir: iacDir, Logger: discardLogger()}
		deployDir := filepath.Join(dir, "deploy", "mystack")
		if err := os.MkdirAll(deployDir, 0755); err != nil {
			t.Fatal(err)
		}

		stack := config.Stack{Path: "nonexistent"}
		err := d.symlinkSource(deployDir, stack)
		if err == nil {
			t.Fatal("expected error for missing path")
		}
		if !strings.Contains(err.Error(), "not found in IaC repo") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestEffectiveRef(t *testing.T) {
	tests := []struct {
		name     string
		stack    config.Stack
		override string
		want     string
	}{
		{
			name:     "override wins over branch",
			stack:    config.Stack{Branch: "main"},
			override: "deploy/v1.2.3",
			want:     "deploy/v1.2.3",
		},
		{
			name:     "override wins over tag",
			stack:    config.Stack{Tag: "v1.0"},
			override: "refs/tags/v2.0",
			want:     "refs/tags/v2.0",
		},
		{
			name:  "tag formatted as refs/tags/<tag>",
			stack: config.Stack{Tag: "v1.0"},
			want:  "refs/tags/v1.0",
		},
		{
			name:  "branch fallback",
			stack: config.Stack{Branch: "main"},
			want:  "main",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveRef(tt.stack, tt.override)
			if got != tt.want {
				t.Errorf("effectiveRef = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveComposePath(t *testing.T) {
	t.Run("absolute path returned as-is", func(t *testing.T) {
		abs := "/absolute/compose.yml"
		got := resolveComposePath(abs, "/repodir")
		if got != abs {
			t.Errorf("got %q, want %q", got, abs)
		}
	})
	t.Run("relative path joined with repoDir", func(t *testing.T) {
		got := resolveComposePath("docker/compose.yml", "/repodir")
		want := "/repodir/docker/compose.yml"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("filename resolved against repoDir", func(t *testing.T) {
		got := resolveComposePath("compose.yml", "/repodir")
		want := "/repodir/compose.yml"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestDefaultPortPerStackType verifies that Deploy selects port 3000 for repo
// stacks and port 80 for path stacks when passing params to GenerateOverride.
func TestDefaultPortPerStackType(t *testing.T) {
	t.Run("repo stack uses port 3000", func(t *testing.T) {
		dir := t.TempDir()
		data, err := GenerateOverride(OverrideParams{
			DeployDir:   dir,
			StackName:   "myapp",
			Domain:      "myapp.example.com",
			ComposeFile: filepath.Join(dir, "nonexistent.yml"),
			DefaultPort: "3000",
			InternalNet: "herald-myapp-internal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "3000") {
			t.Errorf("expected port 3000 in repo stack override:\n%s", data)
		}
	})

	t.Run("path stack uses port 80", func(t *testing.T) {
		dir := t.TempDir()
		data, err := GenerateOverride(OverrideParams{
			DeployDir:   dir,
			StackName:   "myservice",
			Domain:      "myservice.example.com",
			ComposeFile: filepath.Join(dir, "nonexistent.yml"),
			DefaultPort: "80",
			InternalNet: "herald-myservice-internal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "80") {
			t.Errorf("expected port 80 in path stack override:\n%s", data)
		}
	})

	t.Run("env_file includes deployDir/.env", func(t *testing.T) {
		dir := t.TempDir()
		envPath := filepath.Join(dir, ".env")
		data, err := GenerateOverride(OverrideParams{
			DeployDir:    dir,
			StackName:    "myapp",
			Domain:       "myapp.example.com",
			ComposeFile:  filepath.Join(dir, "nonexistent.yml"),
			EnvFilePaths: []string{envPath},
			DefaultPort:  "3000",
			InternalNet:  "herald-myapp-internal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), envPath) {
			t.Errorf("expected %s in override:\n%s", envPath, data)
		}
	})
}

func TestRunPostDeployHook(t *testing.T) {
	t.Run("executes script with correct env vars", func(t *testing.T) {
		dataDir := t.TempDir()
		iacRepo := filepath.Join(dataDir, "repo")
		os.MkdirAll(iacRepo, 0755)

		outFile := filepath.Join(dataDir, "hook_output.txt")
		script := "#!/bin/bash\n" +
			`printf 'STACK_NAME=%s\n' "$STACK_NAME" > ` + outFile + "\n" +
			`printf 'STACK_DIR=%s\n' "$STACK_DIR" >> ` + outFile + "\n" +
			`printf 'STACK_DOMAIN=%s\n' "$STACK_DOMAIN" >> ` + outFile + "\n" +
			`printf 'COMPOSE_FILE=%s\n' "$COMPOSE_FILE" >> ` + outFile + "\n" +
			`printf 'COMPOSE_OVERRIDE_FILE=%s\n' "$COMPOSE_OVERRIDE_FILE" >> ` + outFile + "\n"

		os.WriteFile(filepath.Join(iacRepo, "hook.sh"), []byte(script), 0755)

		deployDir := t.TempDir()
		composeFile := filepath.Join(deployDir, "repo", "compose.yml")

		d := &Deployer{
			Config:  &config.Config{Server: config.Server{ServicesDir: t.TempDir()}},
			Logger:  discardLogger(),
			DataDir: dataDir,
		}

		stack := config.Stack{
			Repo:         "owner/myapp",
			Branch:       "main",
			Domain:       "myapp.example.com",
			UpdateScript: "hook.sh",
		}

		if err := d.runPostDeployHook(context.Background(), "myapp", stack, deployDir, composeFile); err != nil {
			t.Fatalf("runPostDeployHook: %v", err)
		}

		data, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("reading hook output: %v", err)
		}
		content := string(data)

		checks := map[string]string{
			"STACK_NAME":            "myapp",
			"STACK_DIR":             deployDir,
			"STACK_DOMAIN":          "myapp.example.com",
			"COMPOSE_FILE":          composeFile,
			"COMPOSE_OVERRIDE_FILE": filepath.Join(deployDir, "compose.override.yml"),
		}
		for k, v := range checks {
			want := k + "=" + v
			if !strings.Contains(content, want) {
				t.Errorf("hook output missing %q:\n%s", want, content)
			}
		}
	})

	t.Run("missing script returns error", func(t *testing.T) {
		dataDir := t.TempDir()
		os.MkdirAll(filepath.Join(dataDir, "repo"), 0755)

		d := &Deployer{
			Config:  &config.Config{Server: config.Server{ServicesDir: t.TempDir()}},
			Logger:  discardLogger(),
			DataDir: dataDir,
		}
		stack := config.Stack{Domain: "x.example.com", UpdateScript: "nonexistent.sh"}

		err := d.runPostDeployHook(context.Background(), "myapp", stack, t.TempDir(), "compose.yml")
		if err == nil {
			t.Error("expected error for missing update script")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention 'not found', got: %v", err)
		}
	})
}

func TestDown_StackNotFound(t *testing.T) {
	d := &Deployer{
		Config: &config.Config{
			Server: config.Server{ServicesDir: t.TempDir()},
			Stacks: map[string]config.Stack{},
		},
		Logger: discardLogger(),
	}
	err := d.Down(context.Background(), "nonexistent", false)
	if err == nil {
		t.Fatal("expected error for unknown stack")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
