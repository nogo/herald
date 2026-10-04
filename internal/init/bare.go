package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BareOptions configures InitBare.
type BareOptions struct {
	DataDir   string
	HeraldBin string // absolute path the post-receive hook runs
}

// InitBare creates <data_dir>/server.git, clones it into <data_dir>/repo and
// installs a post-receive hook that signals the local daemon. It does nothing
// when both already exist. The repo is group-shared so the operator can push
// and the herald user can read.
func InitBare(ctx context.Context, w io.Writer, opts BareOptions) error {
	bareDir := filepath.Join(opts.DataDir, "server.git")
	repoDir := filepath.Join(opts.DataDir, "repo")

	_, bareErr := os.Stat(filepath.Join(bareDir, "HEAD"))
	_, cloneErr := os.Stat(filepath.Join(repoDir, ".git"))
	if bareErr == nil && cloneErr == nil {
		fmt.Fprintf(w, "Already initialised: %s and %s exist, nothing changed\n", bareDir, repoDir)
		return nil
	}

	if bareErr != nil {
		if err := runGit(ctx, "", "init", "--bare", "--shared=group", "--initial-branch=main", bareDir); err != nil {
			return fmt.Errorf("creating bare repo: %w", err)
		}
	}
	if cloneErr != nil {
		if err := runGit(ctx, "", "clone", bareDir, repoDir); err != nil {
			return fmt.Errorf("cloning bare repo: %w", err)
		}
	}

	hook := fmt.Sprintf("#!/bin/sh\nexec %s signal\n", shellQuote(opts.HeraldBin))
	hookPath := filepath.Join(bareDir, "hooks", "post-receive")
	if err := os.WriteFile(hookPath, []byte(hook), 0755); err != nil {
		return fmt.Errorf("writing post-receive hook: %w", err)
	}

	fmt.Fprintf(w, "Server repo ready: git push <user>@<host>:%s main\n", bareDir)
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
