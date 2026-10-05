package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
)

// BareOptions configures InitBare.
type BareOptions struct {
	DataDir     string
	HeraldBin   string // absolute path the post-receive hook runs
	ServicesDir string
}

// InitBare creates <data_dir>/server.git, clones it into <data_dir>/repo and
// installs a post-receive hook that signals the local daemon. It preserves existing repositories and initializes
// the age key if missing. The repo is group-shared so the operator can push
// and the herald user can read.
func InitBare(ctx context.Context, w io.Writer, opts BareOptions) error {
	absolute, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return err
	}
	opts.DataDir = absolute
	if err := secrets.NewStore(opts.DataDir).Init(); err != nil {
		return err
	}
	layout := config.DataDir(opts.DataDir)
	bareDir, repoDir := layout.BareRepo(), layout.Repo()

	_, bareErr := os.Stat(filepath.Join(bareDir, "HEAD"))
	_, cloneErr := os.Stat(filepath.Join(repoDir, ".git"))
	if bareErr == nil && cloneErr == nil {
		fmt.Fprintf(w, "Already initialised: %s and %s exist, nothing changed\n", bareDir, repoDir)
		printBareCompletion(w, opts, LocalRemoteCommands(opts.DataDir))
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

	// pre-receive rejects a push the daemon would refuse; post-receive tells the
	// daemon about an accepted one.
	for path, command := range map[string]string{layout.PreReceiveHook(): "check-push", layout.PostReceiveHook(): "signal"} {
		hook := fmt.Sprintf("#!/bin/sh\nexec %s %s --data-dir %s\n", shellQuote(opts.HeraldBin), command, shellQuote(opts.DataDir))
		if err := os.WriteFile(path, []byte(hook), 0755); err != nil {
			return fmt.Errorf("writing %s hook: %w", filepath.Base(path), err)
		}
	}

	printBareCompletion(w, opts, LocalRemoteCommands(opts.DataDir))
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
