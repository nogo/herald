package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nogo/herald/internal/maintenance"
	"github.com/nogo/herald/internal/status"
)

// Run the real CLI in a child process so serve can handle SIGTERM without
// changing the test process's signal handlers or Cobra globals.
func TestHeraldProcess(t *testing.T) {
	if os.Getenv("HERALD_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			rootCmd.SetArgs(os.Args[i+1:])
			Execute()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func heraldProcess(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHeraldProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "HERALD_TEST_PROCESS=1")
	return cmd
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for daemon")
		case <-tick.C:
		}
	}
}

func TestServeNonGitHubOriginSignalStatus(t *testing.T) {
	for _, origin := range []string{"/srv/server.git", "ssh://operator@server/srv/server.git"} {
		t.Run(origin, func(t *testing.T) {
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
			write(filepath.Join(repo, "origin"), origin, 0644)
			write(filepath.Join(repo, "head"), "aaaaaaa\n", 0644)
			write(filepath.Join(repo, "upstream-head"), "aaaaaaa\n", 0644)
			cfgPath := filepath.Join(repo, "config.yml")
			configText := "server:\n  name: initial\n  deploy_domain: deploy.example.com\n  acme_email: ops@example.com\n  services_dir: " + dir + "/services\n"
			write(cfgPath, configText, 0644)
			write(filepath.Join(repo, "upstream-config"), configText, 0644)
			// Fake Git reads origin and HEAD from the clone's working directory; pull
			// advances both HEAD and config to the state supplied by the simulated push.
			write(filepath.Join(dir, "git"), `#!/bin/sh
shift 4
case "$1" in
 remote) cat origin ;;
 rev-parse) cat head ;;
 pull) cp upstream-head head; cp upstream-config config.yml ;;
 *) exit 1 ;;
esac
`, 0755)
			write(filepath.Join(dir, "docker"), "#!/bin/sh\nexit 1\n", 0755)
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			p := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			log, err := os.Create(filepath.Join(dir, "daemon.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			daemon := heraldProcess(t, "serve", "--data-dir", dir, "--port", strconv.Itoa(p))
			daemon.Stdout, daemon.Stderr = log, log
			if err := daemon.Start(); err != nil {
				t.Fatal(err)
			}
			stopped := false
			defer func() {
				if !stopped {
					daemon.Process.Kill()
					daemon.Wait()
				}
			}()
			client := &http.Client{Timeout: time.Second}
			waitFor(t, func() bool {
				resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", p))
				if err != nil {
					return false
				}
				defer resp.Body.Close()
				return resp.StatusCode == http.StatusOK
			})
			waitFor(t, func() bool { rep, err := maintenance.LoadReport(dir); return err == nil && rep != nil })
			rep, err := maintenance.LoadReport(dir)
			if err != nil || !rep.Webhooks.Skipped || rep.Webhooks.Error != "" {
				t.Fatalf("startup webhooks: %+v, %v", rep, err)
			}
			write(filepath.Join(repo, "upstream-head"), "bbbbbbb\n", 0644)
			write(filepath.Join(repo, "upstream-config"), strings.Replace(configText, "initial", "updated", 1), 0644)
			output, err := heraldProcess(t, "signal", "--port", strconv.Itoa(p)).CombinedOutput()
			if err != nil || !bytes.Contains(output, []byte("sync submitted")) {
				t.Fatalf("signal: %s, %v", output, err)
			}
			waitFor(t, func() bool {
				rep, err := maintenance.LoadReport(dir)
				return err == nil && rep != nil && rep.IaC.NewHEAD == "bbbbbbb" && rep.Config.Loaded
			})
			output, err = heraldProcess(t, "status", "--data-dir", dir, "--json").Output()
			if err != nil {
				t.Fatalf("status: %s, %v", output, err)
			}
			var snapshot status.ServerStatus
			if err := json.Unmarshal(output, &snapshot); err != nil {
				t.Fatalf("status JSON: %s: %v", output, err)
			}
			if snapshot.IaCCommit != "bbbbbbb" || snapshot.ServerName != "updated" {
				t.Fatalf("status = %+v", snapshot)
			}
			output, err = heraldProcess(t, "status", "--data-dir", dir).CombinedOutput()
			if err != nil || !bytes.Contains(output, []byte("Server repo  bbbbbbb")) {
				t.Fatalf("status: %s, %v", output, err)
			}
			daemon.Process.Signal(os.Interrupt)
			if err := daemon.Wait(); err != nil {
				t.Fatal(err)
			}
			stopped = true
			output, err = heraldProcess(t, "signal", "--port", strconv.Itoa(p)).CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte("connection refused")) || !bytes.Contains(output, []byte("herald serve")) {
				t.Fatalf("offline signal: %s, %v", output, err)
			}
		})
	}
}
