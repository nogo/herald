package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
	bootstrap "github.com/nogo/herald/internal/init"
)

// TestCheckPush_RejectsUnroutablePush pushes through the real hooks of a bare
// server repo, with this test binary standing in for herald.
func TestCheckPush_RejectsUnroutablePush(t *testing.T) {
	dir := t.TempDir()
	herald := filepath.Join(t.TempDir(), "herald")
	wrapper := "#!/bin/sh\nHERALD_TEST_PROCESS=1 exec " + os.Args[0] + " -test.run='^TestHeraldProcess$' -- \"$@\"\n"
	if err := os.WriteFile(herald, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.InitBare(context.Background(), &bytes.Buffer{}, bootstrap.BareOptions{DataDir: dir, HeraldBin: herald}); err != nil {
		t.Fatal(err)
	}
	bare := config.DataDir(dir).BareRepo()

	work := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(work, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=op", "-c", "user.email=op@example.com"}, args...)...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	const cfg = "server:\n  name: pi\n  services_dir: /srv\n  acme_email: ops@example.com\nstacks:\n  shop:\n    path: shop\n    domain: shop.example.com\n"
	write("config.yml", cfg)
	write("shop/compose.yml", "services:\n  web:\n    expose: [\"3000\"]\n  db:\n    image: postgres\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "shop"}} {
		if out, err := git(args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	out, err := git("push", bare, "main")
	if err == nil {
		t.Fatalf("unroutable push was accepted:\n%s", out)
	}
	for _, want := range []string{"push rejected", `stack "shop"`, "shop/compose.yml", "set `service:`"} {
		if !strings.Contains(out, want) {
			t.Errorf("push output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, os.TempDir()+"/herald-push-") {
		t.Errorf("push output names the throwaway checkout:\n%s", out)
	}
	if !bootstrap.NothingPushed(context.Background(), dir) {
		t.Fatal("rejected push still landed in the bare repo")
	}

	write("config.yml", strings.Replace(cfg, "path: shop\n", "path: shop\n    service: web\n", 1))
	if out, err := git("commit", "-q", "-am", "route shop to web"); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if out, err := git("push", bare, "main"); err != nil {
		t.Fatalf("routable push was rejected: %v:\n%s", err, out)
	}

	// Only the deployed branch is checked: a broken experiment elsewhere lands.
	write("config.yml", "not: [valid")
	if out, err := git("commit", "-q", "-am", "break config"); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if out, err := git("push", bare, "HEAD:experiment"); err != nil {
		t.Fatalf("push to another branch was rejected: %v:\n%s", err, out)
	}
}
