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
