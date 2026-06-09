package maintenance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

// mustConfig builds a config whose stacks have the given name → repo mapping.
func mustConfig(t *testing.T, repos map[string]string) *config.Config {
	t.Helper()
	cfg := &config.Config{Stacks: map[string]config.Stack{}}
	for name, repo := range repos {
		cfg.Stacks[name] = config.Stack{Repo: repo}
	}
	return cfg
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPathStackChanged(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	for _, sub := range []string{"appA", "appB"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "file"), []byte("v1"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	c1 := git(t, dir, "rev-parse", "--short", "HEAD")

	// Change appA only, commit.
	if err := os.WriteFile(filepath.Join(dir, "appA", "file"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "bump appA")

	ctx := context.Background()

	if changed, err := pathStackChanged(ctx, dir, c1, "appA"); err != nil || !changed {
		t.Errorf("appA changed since %s: got (%v, %v), want (true, nil)", c1, changed, err)
	}
	if changed, err := pathStackChanged(ctx, dir, c1, "appB"); err != nil || changed {
		t.Errorf("appB changed since %s: got (%v, %v), want (false, nil)", c1, changed, err)
	}
	// No deploy record → treat as changed (deploy once to establish the stamp).
	if changed, err := pathStackChanged(ctx, dir, "", "appB"); err != nil || !changed {
		t.Errorf("empty fromCommit: got (%v, %v), want (true, nil)", changed, err)
	}
	// Unknown commit → error surfaced (caller redeploys to be safe).
	if _, err := pathStackChanged(ctx, dir, "deadbeef", "appA"); err == nil {
		t.Error("unknown fromCommit: expected error, got nil")
	}
}

func TestDesiredRepoSet(t *testing.T) {
	cfg := mustConfig(t, map[string]string{"a": "nogo/app", "b": "nogo/app", "c": "nogo/other"})
	set := desiredRepoSet(cfg, "nogo/iac")
	for _, want := range []string{"nogo/app", "nogo/other", "nogo/iac"} {
		if !set[want] {
			t.Errorf("desiredRepoSet missing %q: %v", want, set)
		}
	}
	if len(set) != 3 {
		t.Errorf("desiredRepoSet size = %d, want 3: %v", len(set), set)
	}

	// No IaC repo: only stack repos.
	if got := desiredRepoSet(cfg, ""); len(got) != 2 {
		t.Errorf("without IaC repo, size = %d, want 2: %v", len(got), got)
	}
}

func TestSameRepoSet(t *testing.T) {
	desired := map[string]bool{"a": true, "b": true}
	cases := []struct {
		name  string
		known map[string]int64
		want  bool
	}{
		{"equal", map[string]int64{"a": 1, "b": 2}, true},
		{"missing", map[string]int64{"a": 1}, false},
		{"extra (repo removed from config)", map[string]int64{"a": 1, "b": 2, "c": 3}, false},
		{"different repo", map[string]int64{"a": 1, "x": 2}, false},
	}
	for _, tc := range cases {
		if got := sameRepoSet(tc.known, desired); got != tc.want {
			t.Errorf("%s: sameRepoSet = %v, want %v", tc.name, got, tc.want)
		}
	}
}
