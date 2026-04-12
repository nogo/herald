package compose

import (
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
