package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestResolve(t *testing.T) {
	t.Run("finds stack before attempting preview lookup", func(t *testing.T) {
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

		ctx, kind, err := Resolve(cfg, dir, "myapp")
		if err != nil {
			t.Fatal(err)
		}
		if kind != "stack" {
			t.Errorf("kind = %q, want %q", kind, "stack")
		}
		if ctx.ProjectName != "herald-myapp" {
			t.Errorf("ProjectName = %q, want %q", ctx.ProjectName, "herald-myapp")
		}
	})

	t.Run("returns error for name not matching any stack or preview", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{
			Server: config.Server{ServicesDir: dir},
			Stacks: map[string]config.Stack{},
		}

		_, _, err := Resolve(cfg, dir, "unknown")
		if err == nil {
			t.Fatal("expected error for unknown name")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention 'not found', got: %v", err)
		}
	})
}

func TestResolveStack_RepoStack(t *testing.T) {
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
	wantCompose := filepath.Join(dir, "myapp", "repo", "compose.yml")
	if ctx.ComposeFile != wantCompose {
		t.Errorf("ComposeFile = %q, want %q", ctx.ComposeFile, wantCompose)
	}
}

func TestResolveStack_PathStack(t *testing.T) {
	dir := t.TempDir()

	// Path stacks use FindComposeFile on the repo dir — create the dir and a compose file.
	repoDir := filepath.Join(dir, "mysvc", "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "compose.yaml"), []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{ServicesDir: dir},
		Stacks: map[string]config.Stack{
			"mysvc": {
				Path:   "services/mysvc",
				Domain: "mysvc.example.com",
			},
		},
	}

	ctx, err := ResolveStack(cfg, "mysvc")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ProjectName != "herald-mysvc" {
		t.Errorf("ProjectName = %q, want %q", ctx.ProjectName, "herald-mysvc")
	}
	wantCompose := filepath.Join(repoDir, "compose.yaml")
	if ctx.ComposeFile != wantCompose {
		t.Errorf("ComposeFile = %q, want %q", ctx.ComposeFile, wantCompose)
	}
}

func TestResolveStack_Unknown(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{ServicesDir: dir},
		Stacks: map[string]config.Stack{},
	}

	_, err := ResolveStack(cfg, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown stack name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}
