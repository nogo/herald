package bootstrap

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestInitBareHookFiresOnPush(t *testing.T) {
	dataDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "signalled")
	fake := filepath.Join(t.TempDir(), "herald's")
	script := "#!/bin/sh\necho \"$@\" > " + marker + "\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := InitBare(context.Background(), &out, BareOptions{DataDir: dataDir, HeraldBin: fake}); err != nil {
		t.Fatal(err)
	}

	bare := filepath.Join(dataDir, "server.git")
	if _, err := os.Stat(filepath.Join(dataDir, "repo", ".git")); err != nil {
		t.Fatalf("repo not cloned: %v", err)
	}
	cfgOut, err := exec.Command("git", "-C", bare, "config", "core.sharedRepository").Output()
	if err != nil || !isGroupShared(strings.TrimSpace(string(cfgOut))) {
		t.Fatalf("core.sharedRepository = %q, %v", cfgOut, err)
	}

	work := t.TempDir()
	gitRun(t, work, "init", "-b", "main")
	gitRun(t, work, "config", "user.email", "op@example.com")
	gitRun(t, work, "config", "user.name", "op")
	if err := os.WriteFile(filepath.Join(work, "config.yml"), []byte("server:\n  name: pi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "config.yml")
	gitRun(t, work, "commit", "-m", "config")
	gitRun(t, work, "push", bare, "main")

	if got, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(got)) != "signal --data-dir "+dataDir {
		t.Fatalf("hook did not run herald signal: %q, %v", got, err)
	}

	// The daemon's clone follows the pushed commit.
	gitRun(t, filepath.Join(dataDir, "repo"), "pull", "--ff-only")
	if _, err := os.Stat(filepath.Join(dataDir, "repo", "config.yml")); err != nil {
		t.Fatalf("config.yml not pulled into clone: %v", err)
	}

	out.Reset()
	if err := InitBare(context.Background(), &out, BareOptions{DataDir: dataDir, HeraldBin: fake}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Already initialised") {
		t.Fatalf("second init output = %q", out.String())
	}
}

// git stores --shared=group as the value "1".
func isGroupShared(v string) bool { return v == "group" || v == "1" }
