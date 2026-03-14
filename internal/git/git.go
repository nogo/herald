// Package git provides authenticated git operations for herald.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CmdWithAuth creates a git command with token-based credential helper.
// Keeps tokens out of URLs, process args, and .git/config.
// Disables git hooks for security.
func CmdWithAuth(ctx context.Context, token, dir string, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		helper := fmt.Sprintf("!f() { echo username=x-access-token; echo password=%s; }; f", token)
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0="+helper,
		)
	}
	return cmd
}

// PullFFOnly runs git pull --ff-only in a directory with auth.
func PullFFOnly(ctx context.Context, token, dir string) (string, error) {
	cmd := CmdWithAuth(ctx, token, dir, "pull", "--ff-only")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RepoURL returns the HTTPS clone URL for a GitHub repo.
func RepoURL(repo string) string {
	return fmt.Sprintf("https://github.com/%s.git", repo)
}

// CloneOrFetch clones the repo if dir does not exist, or fetches and hard-resets to
// origin/<branch> if it does. Uses token-based auth.
func CloneOrFetch(ctx context.Context, token, dir, url, branch string) error {
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		cmd := CmdWithAuth(ctx, token, "", "clone",
			"--branch", branch, "--single-branch", "--depth", "1",
			url, dir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat repo dir: %w", err)
	}

	fetchCmd := CmdWithAuth(ctx, token, dir, "fetch", "origin", branch)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	resetCmd := CmdWithAuth(ctx, token, dir, "reset", "--hard", "origin/"+branch)
	if out, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
