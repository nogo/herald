package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func TestInitBareGuidance(t *testing.T) {
	t.Setenv("SUDO_USER", "operator")
	dir := t.TempDir()
	var out bytes.Buffer
	if err := InitBare(context.Background(), &out, BareOptions{DataDir: dir, HeraldBin: "/bin/true", ServicesDir: "/srv/services"}); err != nil {
		t.Fatal(err)
	}
	name, _ := os.Hostname()
	for _, want := range []string{"git remote add " + name + " ssh://operator@", dir + "/server.git", "git push " + name + " HEAD:main", "mkdir server && cd server", "git init -b main", fmt.Sprintf("name: %q", name), "services_dir: \"/srv/services\"", "acme_email:", "herald status", "sudo systemctl enable --now herald", "Only on-server copies to add to backups:", "Bare repo: " + dir + "/server.git", "Age store: " + dir + "/age.key and " + dir + "/secrets.age"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, &out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "age.key")); err != nil {
		t.Fatal(err)
	}
	if !NothingPushed(context.Background(), dir) {
		t.Fatal("empty bare repo not detected")
	}
	// The printed YAML must satisfy the same validation as a pushed config.
	start := strings.Index(out.String(), "server:\n")
	end := strings.Index(out.String()[start:], "EOF\n") + start
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(out.String()[start:end]), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCommandsLANAndNetBird(t *testing.T) {
	dir := t.TempDir()
	got := remoteCommands("pi", "operator", dir, []string{"192.168.42.10", "100.90.177.232"})
	for i, host := range []string{"192.168.42.10", "100.90.177.232"} {
		want := "git remote add pi ssh://operator@" + host + dir + "/server.git"
		if got[i] != want {
			t.Errorf("command = %q, want %q", got[i], want)
		}
	}
}

func TestGitHubCompletionGuidance(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Config{Server: config.Server{Name: "pi", DeployDomain: "deploy.example.com"}}
	printWebhookSummary(&out, cfg, 1)
	printCompletion(&out, cfg, Options{DataDir: t.TempDir()})
	if !strings.Contains(out.String(), "Webhook: https://deploy.example.com/webhook") {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "herald status") {
		t.Fatal(out.String())
	}
}

func TestRemoteHosts(t *testing.T) {
	interfaces := []net.Interface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "eth0", Flags: net.FlagUp},
		{Name: "wt0", Flags: net.FlagUp},
	}
	addresses := func(iface net.Interface) ([]net.Addr, error) {
		ips := map[string]string{"lo": "127.0.0.1/8", "eth0": "192.168.42.10/24", "wt0": "100.90.177.232/16"}
		parsed, ip, err := net.ParseCIDR(ips[iface.Name])
		ip.IP = parsed
		return []net.Addr{ip}, err
	}
	for _, tc := range []struct {
		count int
		want  string
	}{
		{2, "192.168.42.10"},
		{3, "192.168.42.10,100.90.177.232"},
	} {
		hosts := remoteHosts("pi", interfaces[:tc.count], addresses)
		if got := strings.Join(hosts, ","); got != tc.want {
			t.Errorf("hosts = %q, want %q", got, tc.want)
		}
	}
}

func TestRemoteCommandsUseCurrentUserWithoutSudo(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	operator, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	commands := LocalRemoteCommands(t.TempDir())
	if len(commands) == 0 || !strings.Contains(commands[0], "ssh://"+operator.Username+"@") {
		t.Fatalf("commands = %v", commands)
	}
}

func TestPendingConfig(t *testing.T) {
	ctx := context.Background()
	if _, ok := PendingConfig(ctx, t.TempDir()); ok {
		t.Error("no bare repo: PendingConfig ok = true, want false")
	}

	dataDir := t.TempDir()
	if err := InitBare(ctx, &bytes.Buffer{}, BareOptions{DataDir: dataDir, HeraldBin: "/bin/true"}); err != nil {
		t.Fatal(err)
	}
	cfg, ok := PendingConfig(ctx, dataDir)
	// /opt/deploy is the only stack directory the installed systemd unit lets
	// Herald write; /srv fails the first deploy on a read-only filesystem.
	if !ok || cfg.Server.ServicesDir != "/opt/deploy" || cfg.Server.Port != 9483 {
		t.Fatalf("empty bare repo: PendingConfig = %+v, %v; want /opt/deploy on 9483", cfg, ok)
	}

	work := t.TempDir()
	gitRun(t, work, "init", "-b", "main")
	gitRun(t, work, "-c", "user.email=op@example.com", "-c", "user.name=op", "commit", "--allow-empty", "-m", "first")
	gitRun(t, work, "push", filepath.Join(dataDir, "server.git"), "main")
	if _, ok := PendingConfig(ctx, dataDir); ok {
		t.Error("after a push: PendingConfig ok = true, want false")
	}
}
