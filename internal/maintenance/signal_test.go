package maintenance

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/webhook"
)

func TestLocalSignalPullReloadRedeployChanged(t *testing.T) {
	cfg, _, name := autoDeployConfig(t, false)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "head"), "old\n", 0644)
	if err := os.MkdirAll(filepath.Join(repo, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(repo, "app", "compose.yml"), "services:\n  app:\n    expose: [\"80\"]\n", 0644)
	write(filepath.Join(cfg.Server.ServicesDir, name, "deployed_ref"), "path@old\n", 0644)
	write(filepath.Join(dir, "git"), `#!/bin/sh
shift 4
case "$1" in
 rev-parse) cat head ;;
 pull) echo new > head ;;
 diff) exit 1 ;;
 *) exit 1 ;;
esac
`, 0755)
	write(filepath.Join(dir, "docker"), "#!/bin/sh\nexit 1\n", 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	live := config.NewLive(cfg)
	fd := &fakeDeployer{asyncQueued: true}
	r := &Runner{DataDir: dir, Logger: discardLogger(t), Secrets: secrets.NewStore(dir), Config: live, Deployer: fd,
		Reload: func() (*config.Config, error) {
			head, err := os.ReadFile(filepath.Join(repo, "head"))
			if err != nil || string(head) != "new\n" {
				t.Errorf("reload ran before pull: %q, %v", head, err)
			}
			updated := *cfg
			updated.Server.Name = "reloaded"
			return &updated, nil
		},
	}
	done := make(chan *Report, 1)
	s := &webhook.Server{OnIaCPush: func() {
		done <- r.Run(context.Background(), Options{Pull: true, Webhooks: ReconcileDelta, RedeployChanged: true})
	}}
	req := httptest.NewRequest(http.MethodPost, "/sync", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("signal status = %d", response.Code)
	}
	select {
	case rep := <-done:
		if !rep.IaC.Pulled || rep.IaC.OldHEAD != "old" || rep.IaC.NewHEAD != "new" || !rep.Config.Loaded {
			t.Fatalf("maintenance report = %+v", rep)
		}
		if live.Load().Server.Name != "reloaded" {
			t.Fatal("config reload not published")
		}
		if len(fd.asyncCalls) != 1 || fd.asyncCalls[0] != name {
			t.Fatalf("deploys = %v", fd.asyncCalls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not complete maintenance")
	}
}

func TestReconcileNonGitHubServerRepoSkipped(t *testing.T) {
	cfg := mustConfig(t, nil)
	cfg.Server.GithubToken = "unused-token"
	dir := t.TempDir()
	// A broken state file must never be read when there are no GitHub repos.
	if err := os.WriteFile(filepath.Join(dir, "webhooks.json"), []byte("invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{DataDir: dir, IaCRepo: ""}
	for _, mode := range []Reconcile{ReconcileFull, ReconcileDelta} {
		rep := &Report{}
		r.reconcileWebhooks(context.Background(), cfg, Options{Webhooks: mode}, rep)
		if !rep.Webhooks.Skipped || rep.Webhooks.Error != "" {
			t.Fatalf("webhooks = %+v", rep.Webhooks)
		}
	}
}

func TestMissingConfigBlocksDeploysAndLogsPath(t *testing.T) {
	cfg, _, _ := autoDeployConfig(t, false)
	dir := t.TempDir()
	path := filepath.Join(dir, "repo", "config.yml")
	var logs bytes.Buffer
	fd := &fakeDeployer{asyncQueued: true}
	live := config.NewLive(cfg)
	r := &Runner{DataDir: dir, Logger: slog.New(slog.NewTextHandler(&logs, nil)), Secrets: secrets.NewStore(dir), Config: live, Deployer: fd,
		Reload: func() (*config.Config, error) { return config.Load(path) }}
	rep := r.Run(context.Background(), Options{RedeployChanged: true})
	if rep.Config.Loaded || !strings.Contains(rep.Config.Error, path) {
		t.Fatalf("config = %+v", rep.Config)
	}
	if len(fd.asyncCalls) != 0 || len(fd.deployCalls) != 0 {
		t.Fatalf("deploys = %+v", fd)
	}
	if live.Load() != cfg {
		t.Fatal("bad push replaced live config")
	}
	if !strings.Contains(logs.String(), path) || !strings.Contains(logs.String(), "deploying nothing") {
		t.Fatal(logs.String())
	}
}

func TestUnroutableStackRejectsConfig(t *testing.T) {
	dir := t.TempDir()
	shop := filepath.Join(config.DataDir(dir).Repo(), "shop")
	if err := os.MkdirAll(shop, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shop, "compose.yml"), []byte("services:\n  web:\n    expose: [\"3000\"]\n  db:\n    image: postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := &config.Config{Server: config.Server{ServicesDir: dir}}
	pushed := &config.Config{Server: config.Server{ServicesDir: dir}, Stacks: map[string]config.Stack{"shop": {Path: "shop", Domain: "shop.example.com"}}}
	live := config.NewLive(old)
	fd := &fakeDeployer{}
	r := &Runner{DataDir: dir, Logger: discardLogger(t), Secrets: secrets.NewStore(dir), Config: live, Deployer: fd,
		Reload: func() (*config.Config, error) { return pushed, nil }}

	rep := r.Run(context.Background(), Options{RedeployChanged: true})

	if rep.Config.Loaded || !strings.Contains(rep.Config.Error, `stack "shop"`) || !strings.Contains(rep.Config.Error, "set `service:`") {
		t.Fatalf("config = %+v, want an error naming stack shop and service:", rep.Config)
	}
	if live.Load() != old {
		t.Fatal("unroutable config replaced the live config")
	}
	if len(fd.asyncCalls) != 0 || len(fd.deployCalls) != 0 {
		t.Fatalf("deploys = %+v", fd)
	}
}
