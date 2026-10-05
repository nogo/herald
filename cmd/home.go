package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nogo/herald/internal/config"
	bootstrap "github.com/nogo/herald/internal/init"
	"golang.org/x/term"
)

// banner is the herald's trumpet with its banner hanging below. The site uses
// the same drawing; change both together.
const banner = ` c===========<]   herald %s
      | H |       Your repo. Your server.
      |   |       Herald in between.
      \___/`

// stage is how far this server is through setup, as far as a bare `herald`
// can tell from files and the daemon's /health without asking Docker.
type stage int

const (
	stageNotInstalled   stage = iota // the data dir does not exist
	stageWrongUser                   // the data dir exists, but this user cannot read Herald's files
	stageNotInitialised              // installed, but herald init has not run
	stageAwaitingPush                // herald init made a bare repo; nothing has been pushed to it
	stageConfigBroken                // config.yml exists and does not load
	stageDaemonDown                  // config.yml loads, the daemon does not answer
	stageReady
)

// setup is what a bare `herald` found out about this server.
type setup struct {
	stage   stage
	dataDir string
	server  string
	stacks  int
	port    int
	err     error // why config.yml does not load, for stageConfigBroken
}

// hint is one command to run next and why.
type hint struct {
	cmd  string
	why  string
	more []string // extra lines the command needs, e.g. the git remote to add first
}

// readSetup inspects the data dir and config. daemonUp is asked only once the
// config loads, because only then is the daemon's port known.
func readSetup(ctx context.Context, dataDir, cfgPath string, daemonUp func(port int) bool) setup {
	s := setup{dataDir: dataDir}
	if _, err := os.Stat(dataDir); errors.Is(err, fs.ErrNotExist) {
		s.stage = stageNotInstalled
		return s
	}
	_, err := os.Stat(cfgPath)
	switch {
	case errors.Is(err, fs.ErrPermission):
		s.stage = stageWrongUser
	case err == nil:
		cfg, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			s.stage, s.err = stageConfigBroken, loadErr
			return s
		}
		s.server, s.stacks, s.port = cfg.Server.Name, len(cfg.Stacks), cfg.Server.Port
		s.stage = stageDaemonDown
		if daemonUp(s.port) {
			s.stage = stageReady
		}
	case bootstrap.NothingPushed(ctx, dataDir):
		s.stage = stageAwaitingPush
	case bareRepoExists(dataDir):
		// Pushed, but the daemon has not cloned it yet: it is not running.
		s.stage, s.port = stageDaemonDown, config.DefaultPort
	default:
		s.stage = stageNotInitialised
	}
	return s
}

func bareRepoExists(dataDir string) bool {
	_, err := os.Stat(config.DataDir(dataDir).BareRepo())
	return err == nil
}

// nextStep turns what readSetup found into the one command to run next.
// asOwner prefixes a command that must run as the data dir's owner.
func nextStep(s setup, asOwner func(string) string, remotes []string, hostname string) []hint {
	switch s.stage {
	case stageNotInstalled:
		return []hint{{
			cmd: "curl -fsSL https://raw.githubusercontent.com/nogo/herald/main/scripts/install.sh | sudo sh",
			why: fmt.Sprintf("Herald is not installed here: %s does not exist (or pass --data-dir)", s.dataDir),
		}}
	case stageWrongUser:
		cmd := asOwner("herald")
		if cmd == "herald" {
			// Same owner but still unreadable: the files' mode is off. Point at the
			// user install.sh creates.
			cmd = "sudo -iu herald herald"
		}
		return []hint{{cmd: cmd, why: "Herald's files belong to another user; run it as that user"}}
	case stageNotInitialised:
		return []hint{
			{cmd: asOwner("herald init owner/server-repo"), why: "connect the GitHub repo that describes this server"},
			{cmd: asOwner("herald init"), why: "or keep the server repo on this server and push to it over SSH"},
		}
	case stageAwaitingPush:
		push := hint{cmd: fmt.Sprintf("git push %s HEAD:main", hostname), why: "then push it; Herald deploys what config.yml describes"}
		if len(remotes) == 0 {
			push.why = "on your laptop, after adding this server as a git remote"
			return []hint{push}
		}
		add := hint{cmd: remotes[0], why: "on your laptop, in your server repo"}
		if len(remotes) > 1 {
			add.why = "on your laptop, in your server repo; or, on another network:"
			add.more = remotes[1:]
		}
		return []hint{add, push}
	case stageConfigBroken:
		return []hint{{cmd: asOwner("herald doctor"), why: firstLine(s.err)}}
	case stageDaemonDown:
		return []hint{{cmd: "sudo systemctl enable --now herald", why: fmt.Sprintf("the daemon is not answering on port %d", s.port)}}
	default:
		return []hint{
			{cmd: asOwner("herald status"), why: "every stack, its domain and its health"},
			{cmd: asOwner("herald doctor"), why: "when something does not deploy"},
		}
	}
}

func firstLine(err error) string {
	if err == nil {
		return ""
	}
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}

// renderHome writes the banner, a one-line summary of the server and the next
// steps. color is false when w is not a terminal or NO_COLOR is set.
func renderHome(w io.Writer, s setup, hints []hint, version string, color bool) {
	st := style{on: color}
	fmt.Fprintln(w, st.accent(fmt.Sprintf(banner, version)))
	fmt.Fprintln(w)
	if summary := summarize(s); summary != "" {
		fmt.Fprintf(w, "  %s   %s\n", st.dim("server"), summary)
	}
	for i, h := range hints {
		label := "      "
		if i == 0 {
			label = st.dim("next") + "  "
		}
		fmt.Fprintf(w, "  %s   %s\n", label, st.bold(h.cmd))
		if h.why != "" {
			fmt.Fprintf(w, "           %s\n", st.dim(h.why))
		}
		for _, m := range h.more {
			fmt.Fprintf(w, "           %s\n", m)
		}
	}
	fmt.Fprintf(w, "\n  %s\n", st.dim("herald --help lists every command"))
}

func summarize(s setup) string {
	switch s.stage {
	case stageReady, stageDaemonDown:
		daemon := "daemon running"
		if s.stage == stageDaemonDown {
			daemon = "daemon not running"
		}
		if s.server == "" {
			return daemon
		}
		return fmt.Sprintf("%s · %s · %s", s.server, plural(s.stacks, "stack"), daemon)
	case stageAwaitingPush:
		return "waiting for the first push of the server repo"
	case stageConfigBroken:
		return "config.yml does not load"
	case stageNotInitialised:
		return "installed, not set up yet"
	case stageWrongUser:
		return "this user cannot read Herald's files"
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// style adds ANSI colour when on is true and returns text unchanged otherwise.
type style struct{ on bool }

func (s style) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return code + text + "\033[0m"
}
func (s style) accent(t string) string { return s.wrap("\033[33m", t) }
func (s style) bold(t string) string   { return s.wrap("\033[1m", t) }
func (s style) dim(t string) string    { return s.wrap("\033[2m", t) }

// colorOn reports whether output to w should be coloured: w is a terminal and
// NO_COLOR (no-color.org) is not set.
func colorOn(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
}

// ownerPrefix returns a function that prefixes a command with `sudo -iu <owner>`
// when the data dir belongs to another user, so a copied command runs as the
// user that owns Herald's files.
func ownerPrefix(dataDir string) func(string) string {
	same := func(c string) string { return c }
	info, err := os.Stat(dataDir)
	if err != nil {
		return same
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		return same
	}
	owner, err := user.LookupId(strconv.Itoa(int(st.Uid)))
	if err != nil {
		return same
	}
	return func(c string) string { return "sudo -iu " + owner.Username + " " + c }
}

// daemonHealthy asks the local daemon's /health. Like `herald signal`, it
// bypasses HTTP proxies to reach loopback directly.
func daemonHealthy(ctx context.Context, port int) bool {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// homeConfigPath is the config a bare `herald` inspects: --config when given,
// otherwise the server repo clone's config.yml.
func homeConfigPath() string {
	if rootCmd.PersistentFlags().Changed("config") {
		return cfgFile
	}
	return config.DataDir(dataDir).ConfigFile()
}

func runHome(ctx context.Context, w io.Writer) {
	s := readSetup(ctx, dataDir, homeConfigPath(), func(port int) bool { return daemonHealthy(ctx, port) })
	var remotes []string
	if s.stage == stageAwaitingPush {
		remotes = bootstrap.LocalRemoteCommands(dataDir)
	}
	hostname, _ := os.Hostname()
	renderHome(w, s, nextStep(s, ownerPrefix(dataDir), remotes, hostname), shortVersion(), colorOn(w))
}
