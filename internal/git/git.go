// Package git provides authenticated git operations for herald.
package git

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CmdWithAuth creates a git command with token-based credential helper.
// Keeps tokens out of URLs, process args, and .git/config.
// Disables git hooks for security.
func CmdWithAuth(token, dir string, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.Command("git", gitArgs...)
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
func PullFFOnly(token, dir string) (string, error) {
	cmd := CmdWithAuth(token, dir, "pull", "--ff-only")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RepoURL returns the HTTPS clone URL for a GitHub repo.
func RepoURL(repo string) string {
	return fmt.Sprintf("https://github.com/%s.git", repo)
}
