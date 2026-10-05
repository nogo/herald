package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
)

const validConfig = "server:\n  name: pi\n  services_dir: /srv\n  acme_email: ops@example.com\n  port: 9999\nstacks:\n  wiki:\n    path: stacks/wiki\n    domain: wiki.example.com\n"

func writeConfig(t *testing.T, dataDir, body string) string {
	t.Helper()
	path := config.DataDir(dataDir).ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSetup(t *testing.T) {
	up := func(int) bool { return true }
	down := func(int) bool { return false }

	tests := []struct {
		name    string
		prepare func(t *testing.T, dataDir string)
		daemon  func(int) bool
		want    stage
	}{
		{"no data dir", func(t *testing.T, d string) { os.Remove(d) }, up, stageNotInstalled},
		{"empty data dir", func(t *testing.T, d string) {}, up, stageNotInitialised},
		{"config does not load", func(t *testing.T, d string) { writeConfig(t, d, "server: [\n") }, up, stageConfigBroken},
		{"config loads, daemon down", func(t *testing.T, d string) { writeConfig(t, d, validConfig) }, down, stageDaemonDown},
		{"config loads, daemon up", func(t *testing.T, d string) { writeConfig(t, d, validConfig) }, up, stageReady},
		{"bare repo, nothing pushed", func(t *testing.T, d string) { makeBareRepo(t, d) }, up, stageAwaitingPush},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			tt.prepare(t, dataDir)
			got := readSetup(context.Background(), dataDir, config.DataDir(dataDir).ConfigFile(), tt.daemon)
			if got.stage != tt.want {
				t.Errorf("stage = %d, want %d (err %v)", got.stage, tt.want, got.err)
			}
		})
	}
}

func TestReadSetup_UnreadableConfigIsWrongUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads any file")
	}
	dataDir := t.TempDir()
	path := writeConfig(t, dataDir, validConfig)
	repo := filepath.Dir(path)
	if err := os.Chmod(repo, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(repo, 0o755) })

	got := readSetup(context.Background(), dataDir, path, func(int) bool { return true })
	if got.stage != stageWrongUser {
		t.Errorf("stage = %d, want stageWrongUser", got.stage)
	}
}

func TestReadSetup_ReadyReportsServerAndStacks(t *testing.T) {
	dataDir := t.TempDir()
	path := writeConfig(t, dataDir, validConfig)
	var asked int
	got := readSetup(context.Background(), dataDir, path, func(port int) bool { asked = port; return true })
	if got.server != "pi" || got.stacks != 1 {
		t.Errorf("server, stacks = %q, %d; want pi, 1", got.server, got.stacks)
	}
	if asked != 9999 {
		t.Errorf("asked the daemon on port %d, want server.port 9999", asked)
	}
}

func makeBareRepo(t *testing.T, dataDir string) {
	t.Helper()
	if err := exec.Command("git", "init", "--bare", "-q", config.DataDir(dataDir).BareRepo()).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func TestNextStep_PrefixesCommandsForTheOwner(t *testing.T) {
	asOwner := func(c string) string { return "sudo -iu herald " + c }
	hints := nextStep(setup{stage: stageNotInitialised}, asOwner, nil, "pi")
	if hints[0].cmd != "sudo -iu herald herald init owner/server-repo" {
		t.Errorf("cmd = %q", hints[0].cmd)
	}
}

func TestNextStep_EveryStageNamesACommand(t *testing.T) {
	same := func(c string) string { return c }
	for st := stageNotInstalled; st <= stageReady; st++ {
		hints := nextStep(setup{stage: st, err: errors.New("bad\nmore")}, same, []string{"git remote add pi ssh://x"}, "pi")
		if len(hints) == 0 || hints[0].cmd == "" || hints[0].why == "" {
			t.Errorf("stage %d: hints = %+v, want a command and a reason", st, hints)
		}
	}
}

func TestNextStep_ConfigBrokenShowsOnlyTheFirstErrorLine(t *testing.T) {
	hints := nextStep(setup{stage: stageConfigBroken, err: errors.New("stacks.wiki.domain is required\n  at line 4")}, func(c string) string { return c }, nil, "pi")
	if hints[0].cmd != "herald doctor" || hints[0].why != "stacks.wiki.domain is required" {
		t.Errorf("hint = %+v", hints[0])
	}
}

func TestRenderHome_PlainWithoutColor(t *testing.T) {
	var b bytes.Buffer
	s := setup{stage: stageReady, server: "pi", stacks: 2}
	renderHome(&b, s, nextStep(s, func(c string) string { return c }, nil, "pi"), "v4.0.0", false)
	out := b.String()
	for _, want := range []string{"herald v4.0.0", "Herald in between.", "pi · 2 stacks · daemon running", "next     herald status"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[") {
		t.Errorf("output has ANSI codes with color off:\n%s", out)
	}
}
