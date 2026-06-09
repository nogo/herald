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
	gitArgs := append([]string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "safe.directory=*",
	}, args...)
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

// ValidateRef rejects git refs that could be misinterpreted as command-line
// options (argument injection, e.g. "--upload-pack=...") or that contain unsafe
// characters. It allows branch names, tag names, and full refspecs such as
// "refs/tags/v1.2.3". Refs reaching git from webhooks (preview branches) are
// partly attacker-influenced, so they are validated before use.
func ValidateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty git ref")
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid git ref %q: must not begin with '-'", ref)
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/' || r == '+':
		default:
			return fmt.Errorf("invalid git ref %q: contains unsupported character %q", ref, string(r))
		}
	}
	return nil
}

// CloneOrFetch clones the repo if dir does not exist, or fetches and hard-resets to
// FETCH_HEAD if it does. ref may be a branch name ("main") or full tag refspec
// ("refs/tags/v1.2.3"). Uses token-based auth.
func CloneOrFetch(ctx context.Context, token, dir, url, ref string) error {
	if err := ValidateRef(ref); err != nil {
		return err
	}
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		// git clone --branch accepts branch names and tag names, not full refspecs.
		cloneRef := ref
		if strings.HasPrefix(ref, "refs/tags/") {
			cloneRef = strings.TrimPrefix(ref, "refs/tags/")
		}
		cmd := CmdWithAuth(ctx, token, "", "clone",
			"--branch", cloneRef, "--single-branch", "--depth", "1",
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

	// git fetch origin <ref> works for both branch names and refs/tags/v1.2.3.
	fetchCmd := CmdWithAuth(ctx, token, dir, "fetch", "origin", ref)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	resetCmd := CmdWithAuth(ctx, token, dir, "reset", "--hard", "FETCH_HEAD")
	if out, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
