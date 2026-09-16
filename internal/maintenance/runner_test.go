package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
)

func discardLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(100)}))
}

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

// fakeDeployer is a stackDeployer test double: it records what was requested
// and lets the test dictate the outcome, so surveyStacks' handling of deploy
// results can be tested without a real Docker host.
type fakeDeployer struct {
	deployErr   error
	asyncQueued bool

	deployCalls []string
	asyncCalls  []string
}

func (f *fakeDeployer) Deploy(_ context.Context, stackName, _ string) error {
	f.deployCalls = append(f.deployCalls, stackName)
	return f.deployErr
}

func (f *fakeDeployer) DeployAsync(stackName, _ string) bool {
	f.asyncCalls = append(f.asyncCalls, stackName)
	return f.asyncQueued
}

func (f *fakeDeployer) SetConfig(*config.Config) {}

// autoDeployConfig builds a config with a single auto-deploy path stack whose
// deploy directory already exists (so surveyStacks reaches the deploy branch)
// and, when drifted is true, whose recorded config fingerprint no longer
// matches the stack (so ConfigDrift starts true).
func autoDeployConfig(t *testing.T, drifted bool) (*config.Config, config.Stack, string) {
	t.Helper()
	servicesDir := t.TempDir()
	name := "app"
	stack := config.Stack{Path: "app", AutoDeploy: true}

	deployDir := filepath.Join(servicesDir, name)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		t.Fatal(err)
	}
	if drifted {
		if err := os.WriteFile(filepath.Join(deployDir, "deployed_config"), []byte("stale-hash"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Server: config.Server{ServicesDir: servicesDir},
		Stacks: map[string]config.Stack{name: stack},
	}
	return cfg, stack, name
}

func TestSurveyStacksSyncDeploySuccess(t *testing.T) {
	cfg, _, name := autoDeployConfig(t, true)
	fd := &fakeDeployer{}
	r := &Runner{
		DataDir:  t.TempDir(),
		Logger:   discardLogger(t),
		Secrets:  secrets.NewStore(t.TempDir()),
		Deployer: fd,
	}
	rep := &Report{}
	r.surveyStacks(context.Background(), cfg, Options{RedeployChanged: true, BlockOnDeploys: true}, true, rep)

	if len(fd.deployCalls) != 1 || len(fd.asyncCalls) != 0 {
		t.Fatalf("expected one synchronous deploy call, got sync=%v async=%v", fd.deployCalls, fd.asyncCalls)
	}
	sr := rep.Stacks[0]
	if sr.Name != name || sr.Action != "redeployed" {
		t.Errorf("got Action %q, want %q", sr.Action, "redeployed")
	}
	if sr.Detail != "" {
		t.Errorf("got Detail %q, want empty on success", sr.Detail)
	}
	if sr.ConfigDrift {
		t.Error("ConfigDrift should be cleared after a confirmed successful deploy")
	}
}

func TestSurveyStacksSyncDeployFailure(t *testing.T) {
	cfg, _, _ := autoDeployConfig(t, true)
	fd := &fakeDeployer{deployErr: errors.New("compose up: boom")}
	r := &Runner{
		DataDir:  t.TempDir(),
		Logger:   discardLogger(t),
		Secrets:  secrets.NewStore(t.TempDir()),
		Deployer: fd,
	}
	rep := &Report{}
	r.surveyStacks(context.Background(), cfg, Options{RedeployChanged: true, BlockOnDeploys: true}, true, rep)

	sr := rep.Stacks[0]
	if sr.Action == "redeployed" {
		t.Error("a failed deploy must not be recorded as redeployed")
	}
	if sr.Action != "deploy failed" {
		t.Errorf("got Action %q, want %q", sr.Action, "deploy failed")
	}
	if !strings.Contains(sr.Detail, "boom") {
		t.Errorf("Detail %q does not surface the deploy error", sr.Detail)
	}
	if !sr.ConfigDrift {
		t.Error("ConfigDrift must not be cleared merely because a failed deployment was attempted")
	}
	if !rep.Failed() {
		t.Error("Report.Failed() should be true when a stack recorded a failed deploy")
	}
}

func TestSurveyStacksAsyncSubmission(t *testing.T) {
	cfg, _, _ := autoDeployConfig(t, true)
	fd := &fakeDeployer{asyncQueued: true}
	r := &Runner{
		DataDir:  t.TempDir(),
		Logger:   discardLogger(t),
		Secrets:  secrets.NewStore(t.TempDir()),
		Deployer: fd,
	}
	rep := &Report{}
	// BlockOnDeploys is false: the daemon path, which dispatches and moves on.
	r.surveyStacks(context.Background(), cfg, Options{RedeployChanged: true}, true, rep)

	if len(fd.asyncCalls) != 1 || len(fd.deployCalls) != 0 {
		t.Fatalf("expected one async submission, got sync=%v async=%v", fd.deployCalls, fd.asyncCalls)
	}
	sr := rep.Stacks[0]
	if sr.Action == "redeployed" {
		t.Error("an async submission must never be reported as completed")
	}
	if sr.Action != "deploy queued" {
		t.Errorf("got Action %q, want %q", sr.Action, "deploy queued")
	}
	if !sr.ConfigDrift {
		t.Error("ConfigDrift must stay set until the async result is known")
	}
	if rep.Failed() {
		t.Error("a queued submission is not a failure")
	}
}

func TestSurveyStacksAsyncDropped(t *testing.T) {
	cfg, _, _ := autoDeployConfig(t, false)
	fd := &fakeDeployer{asyncQueued: false}
	r := &Runner{
		DataDir:  t.TempDir(),
		Logger:   discardLogger(t),
		Secrets:  secrets.NewStore(t.TempDir()),
		Deployer: fd,
	}
	rep := &Report{}
	r.surveyStacks(context.Background(), cfg, Options{RedeployChanged: true}, true, rep)

	sr := rep.Stacks[0]
	if sr.Action != "deploy dropped" {
		t.Errorf("got Action %q, want %q", sr.Action, "deploy dropped")
	}
	if sr.Detail == "" {
		t.Error("a dropped submission should explain why in Detail")
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
