package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
)

// fakeCommit is what the fake git prints for `rev-parse --short HEAD`.
const fakeCommit = "abc1234"

// pipelineEnv is a deploy sandbox: a data dir with an initialised secrets store,
// a services dir, and fake docker/git binaries on PATH that log every call.
type pipelineEnv struct {
	dataDir     string
	servicesDir string
	dockerLog   string
	store       *secrets.Store
}

func newPipelineEnv(t *testing.T) *pipelineEnv {
	t.Helper()
	root := t.TempDir()
	e := &pipelineEnv{
		dataDir:     filepath.Join(root, "data"),
		servicesDir: filepath.Join(root, "deploy"),
		dockerLog:   filepath.Join(root, "docker.log"),
	}
	if err := os.MkdirAll(e.dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	e.store = secrets.NewStore(e.dataDir)
	if err := e.store.Init(); err != nil {
		t.Fatal(err)
	}
	installPipelineFakes(t, e.dockerLog)
	return e
}

// installPipelineFakes puts fake docker and git binaries at the front of PATH.
//
// docker appends its arguments to logPath, one call per line. A call containing
// " up " fails when HERALD_TEST_DOCKER_FAIL_UP=1, and blocks until the file named
// by HERALD_TEST_DOCKER_GATE exists when that variable is set.
//
// git creates the destination with a one-service compose.yml on clone, prints
// fakeCommit for rev-parse, and succeeds for everything else.
func installPipelineFakes(t *testing.T, logPath string) {
	t.Helper()
	bin := t.TempDir()
	docker := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case " $* " in
*" up "*)
	if [ "$HERALD_TEST_DOCKER_FAIL_UP" = "1" ]; then
		echo fake compose up failure >&2
		exit 1
	fi
	if [ -n "$HERALD_TEST_DOCKER_GATE" ]; then
		while [ ! -f "$HERALD_TEST_DOCKER_GATE" ]; do sleep 0.02; done
	fi
	;;
esac
exit 0
`, logPath)
	git := `#!/bin/sh
mode=""
last=""
for a in "$@"; do
	case "$a" in
	clone) mode=clone ;;
	rev-parse) mode=rev-parse ;;
	esac
	last="$a"
done
case "$mode" in
clone)
	mkdir -p "$last"
	printf 'services:\n  web:\n    image: example/web\n' > "$last/compose.yml"
	;;
rev-parse) echo ` + fakeCommit + ` ;;
esac
exit 0
`
	for name, script := range map[string]string{"docker": docker, "git": git} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (e *pipelineEnv) deployer(stacks map[string]config.Stack) *Deployer {
	return &Deployer{
		Config: &config.Config{
			Server: config.Server{ServicesDir: e.servicesDir},
			Stacks: stacks,
		},
		Secrets: e.store,
		Logger:  discardLogger(),
		DataDir: e.dataDir,
	}
}

// dockerCalls returns the logged docker invocations, one per element.
func (e *pipelineEnv) dockerCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.dockerLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (e *pipelineEnv) composeUpCalls(t *testing.T) []string {
	t.Helper()
	var ups []string
	for _, c := range e.dockerCalls(t) {
		if strings.Contains(" "+c+" ", " up ") {
			ups = append(ups, c)
		}
	}
	return ups
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func repoStack() config.Stack {
	return config.Stack{
		Repo:    "acme/app",
		Branch:  "main",
		Compose: "compose.yml",
		Domain:  "app.example.com",
		Secrets: []config.SecretRef{
			{Key: "app/db_password", Type: "env", Target: "DB_PASSWORD"},
			{Key: "app/api_key", Type: "docker-secret", Target: "api_key"},
		},
	}
}

func TestDeploy_RepoStack(t *testing.T) {
	e := newPipelineEnv(t)
	if err := e.store.Set("app/db_password", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set("app/api_key", "k-123"); err != nil {
		t.Fatal(err)
	}
	stack := repoStack()
	d := e.deployer(map[string]config.Stack{"app": stack})

	if err := d.Deploy(context.Background(), "app", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	dir := filepath.Join(e.servicesDir, "app")
	if env := readFile(t, filepath.Join(dir, ".env")); !strings.Contains(env, "DB_PASSWORD=hunter2") {
		t.Errorf(".env missing resolved env secret:\n%s", env)
	}
	if got := readFile(t, filepath.Join(dir, "secrets", "api_key")); got != "k-123" {
		t.Errorf("docker secret file = %q, want %q", got, "k-123")
	}
	override := readFile(t, filepath.Join(dir, "compose.override.yml"))
	for _, want := range []string{"caddy: app.example.com", "{{upstreams 3000}}", "herald-app-internal"} {
		if !strings.Contains(override, want) {
			t.Errorf("override missing %q:\n%s", want, override)
		}
	}

	wantUp := fmt.Sprintf("compose --project-name herald-app --env-file %s -f %s -f %s --progress plain up -d --build --remove-orphans",
		filepath.Join(dir, ".env"), filepath.Join(dir, "repo", "compose.yml"), filepath.Join(dir, "compose.override.yml"))
	if ups := e.composeUpCalls(t); len(ups) != 1 || ups[0] != wantUp {
		t.Errorf("compose up calls = %q, want [%q]", ups, wantUp)
	}

	if got := strings.TrimSpace(readFile(t, filepath.Join(dir, "deployed_ref"))); got != "main@"+fakeCommit {
		t.Errorf("deployed_ref = %q, want %q", got, "main@"+fakeCommit)
	}
	if ConfigDrifted(dir, stack) {
		t.Error("ConfigDrifted = true right after a deploy")
	}
}

func TestDeploy_RefOverridesBranch(t *testing.T) {
	e := newPipelineEnv(t)
	stack := repoStack()
	stack.Secrets = nil
	d := e.deployer(map[string]config.Stack{"app": stack})

	if err := d.Deploy(context.Background(), "app", "refs/tags/v1.2.0"); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	got := strings.TrimSpace(readFile(t, filepath.Join(e.servicesDir, "app", "deployed_ref")))
	if want := "refs/tags/v1.2.0@" + fakeCommit; got != want {
		t.Errorf("deployed_ref = %q, want %q", got, want)
	}
}

func TestDeploy_PathStack(t *testing.T) {
	e := newPipelineEnv(t)
	src := filepath.Join(e.dataDir, "repo", "stacks", "wiki")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "compose.yml"), []byte("services:\n  wiki:\n    image: example/wiki\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d := e.deployer(map[string]config.Stack{"wiki": {Path: "stacks/wiki", Domain: "wiki.example.com"}})

	if err := d.Deploy(context.Background(), "wiki", ""); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	dir := filepath.Join(e.servicesDir, "wiki")
	if link, err := os.Readlink(filepath.Join(dir, "repo")); err != nil || link != src {
		t.Errorf("repo symlink = %q (%v), want %q", link, err, src)
	}
	if override := readFile(t, filepath.Join(dir, "compose.override.yml")); !strings.Contains(override, "{{upstreams 80}}") {
		t.Errorf("path stack override should default to port 80:\n%s", override)
	}
	if ups := e.composeUpCalls(t); len(ups) != 1 || !strings.Contains(ups[0], "--project-name herald-wiki ") {
		t.Errorf("compose up calls = %q, want one for herald-wiki", ups)
	}
	if got := ReadDeployedIaCCommit(dir); got != fakeCommit {
		t.Errorf("ReadDeployedIaCCommit = %q, want %q", got, fakeCommit)
	}
}

func TestDeploy_MissingSecretChangesNothing(t *testing.T) {
	e := newPipelineEnv(t)
	d := e.deployer(map[string]config.Stack{"app": repoStack()})

	err := d.Deploy(context.Background(), "app", "")
	if err == nil || !strings.Contains(err.Error(), "app/db_password") {
		t.Fatalf("Deploy error = %v, want one naming the missing secret", err)
	}

	if _, err := os.Stat(filepath.Join(e.servicesDir, "app")); !os.IsNotExist(err) {
		t.Errorf("deploy dir exists after preflight failure (stat err %v)", err)
	}
	if calls := e.dockerCalls(t); len(calls) != 0 {
		t.Errorf("docker called after preflight failure: %q", calls)
	}
}

func TestDeploy_ComposeUpFailureRecordsNoDeploy(t *testing.T) {
	e := newPipelineEnv(t)
	t.Setenv("HERALD_TEST_DOCKER_FAIL_UP", "1")
	stack := repoStack()
	stack.Secrets = nil
	d := e.deployer(map[string]config.Stack{"app": stack})

	if err := d.Deploy(context.Background(), "app", ""); err == nil {
		t.Fatal("Deploy succeeded although compose up failed")
	}

	dir := filepath.Join(e.servicesDir, "app")
	for _, stamp := range []string{"deployed_ref", "deployed_config"} {
		if _, err := os.Stat(filepath.Join(dir, stamp)); !os.IsNotExist(err) {
			t.Errorf("%s written for a failed deploy (stat err %v)", stamp, err)
		}
	}
}

func TestDeploy_UnknownStack(t *testing.T) {
	e := newPipelineEnv(t)
	d := e.deployer(map[string]config.Stack{})

	if err := d.Deploy(context.Background(), "ghost", ""); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Deploy error = %v, want one naming the stack", err)
	}
}

func TestDeployAsync_QueuesOneAndDropsTheRest(t *testing.T) {
	e := newPipelineEnv(t)
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv("HERALD_TEST_DOCKER_GATE", gate)
	stack := repoStack()
	stack.Secrets = nil
	d := e.deployer(map[string]config.Stack{"app": stack})

	// The first deploy holds at compose up until the gate opens, so the second
	// waits behind it and the third finds the queue full.
	first := d.DeployAsync("app", "")
	second := d.DeployAsync("app", "")
	third := d.DeployAsync("app", "")
	if err := os.WriteFile(gate, nil, 0644); err != nil {
		t.Fatal(err)
	}
	d.Wait()

	if !first || !second || third {
		t.Errorf("DeployAsync results = %v, %v, %v; want true, true, false", first, second, third)
	}
	if ups := e.composeUpCalls(t); len(ups) != 2 {
		t.Errorf("compose up ran %d times, want 2", len(ups))
	}
}

func TestDeployAsync_WaitsForLimiterCapacity(t *testing.T) {
	e := newPipelineEnv(t)
	stack := repoStack()
	stack.Secrets = nil
	d := e.deployer(map[string]config.Stack{"app": stack})
	d.Limiter = NewLimiter(1)
	d.Limiter.Close()

	if !d.DeployAsync("app", "") {
		t.Fatal("DeployAsync = false, want the deploy submitted")
	}
	d.Wait()

	if calls := e.dockerCalls(t); len(calls) != 0 {
		t.Errorf("deploy ran although the limiter admitted nothing: %q", calls)
	}
}

// In the daemon there is no UI stream, so compose output must reach the log —
// otherwise a failed webhook deploy leaves only "exit status 1" in the journal.
func TestDeploy_DaemonLogsComposeOutputOnFailure(t *testing.T) {
	e := newPipelineEnv(t)
	t.Setenv("HERALD_TEST_DOCKER_FAIL_UP", "1")
	stack := repoStack()
	stack.Secrets = nil
	d := e.deployer(map[string]config.Stack{"app": stack})
	var logs strings.Builder
	d.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	if err := d.Deploy(context.Background(), "app", ""); err == nil {
		t.Fatal("Deploy succeeded although compose up failed")
	}

	if !strings.Contains(logs.String(), "fake compose up failure") {
		t.Errorf("compose stderr missing from daemon log:\n%s", logs.String())
	}
}
