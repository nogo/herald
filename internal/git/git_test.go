package git

import (
	"testing"
)

func TestRepoURL(t *testing.T) {
	tests := []struct {
		repo, want string
	}{
		{"owner/repo", "https://github.com/owner/repo.git"},
		{"nogo/budget-app", "https://github.com/nogo/budget-app.git"},
	}
	for _, tc := range tests {
		got := RepoURL(tc.repo)
		if got != tc.want {
			t.Errorf("RepoURL(%q) = %q, want %q", tc.repo, got, tc.want)
		}
	}
}

func TestCmdWithAuth(t *testing.T) {
	ctx := t.Context()

	// Without token: no credential helper env vars
	cmd := CmdWithAuth(ctx, "", "/tmp", "status")
	hasCredHelper := false
	for _, e := range cmd.Env {
		if e == "GIT_CONFIG_COUNT=1" {
			hasCredHelper = true
		}
	}
	if hasCredHelper {
		t.Error("expected no credential helper without token")
	}

	// With token: credential helper set
	cmd = CmdWithAuth(ctx, "ghp_test", "/tmp", "clone", "url")
	hasCredHelper = false
	for _, e := range cmd.Env {
		if e == "GIT_CONFIG_COUNT=1" {
			hasCredHelper = true
		}
	}
	if !hasCredHelper {
		t.Error("expected credential helper with token")
	}

	// Verify hooks are disabled
	if cmd.Args[1] != "-c" || cmd.Args[2] != "core.hooksPath=/dev/null" {
		t.Errorf("expected hooks disabled, got args: %v", cmd.Args[:4])
	}
}
