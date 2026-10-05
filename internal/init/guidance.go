package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/nogo/herald/internal/config"
)

// LocalRemoteCommands gives the operator one remote command per reachable network.
func LocalRemoteCommands(dataDir string) []string {
	name, _ := os.Hostname()
	operator := os.Getenv("SUDO_USER")
	if operator == "" {
		if u, err := user.Current(); err == nil {
			operator = u.Username
		}
	}
	interfaces, _ := net.Interfaces()
	return remoteCommands(name, operator, dataDir, remoteHosts(name, interfaces, func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() }))
}

func remoteHosts(name string, interfaces []net.Interface, addresses func(net.Interface) ([]net.Addr, error)) []string {
	hosts := []string{}
	lan, netbird := "", ""
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := addresses(iface)
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
				continue
			}
			if iface.Name == "wt0" || strings.HasPrefix(strings.ToLower(iface.Name), "netbird") {
				netbird = ip.String()
			} else if lan == "" {
				lan = ip.String()
			}
		}
	}
	if lan != "" {
		hosts = append(hosts, lan)
	} else {
		hosts = append(hosts, name)
	}
	if netbird != "" {
		hosts = append(hosts, netbird)
	}
	return hosts
}

func remoteCommands(name, operator, dataDir string, hosts []string) []string {
	absolute, _ := filepath.Abs(filepath.Join(dataDir, "server.git"))
	commands := []string{}
	for _, host := range hosts {
		remote := url.URL{Scheme: "ssh", User: url.User(operator), Host: host, Path: absolute}
		commands = append(commands, "git remote add "+name+" "+remote.String())
	}
	return commands
}

// NothingPushed checks the bare repo itself, rather than a clone that may be stale.
func NothingPushed(ctx context.Context, dataDir string) bool {
	bare := filepath.Join(dataDir, "server.git")
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", bare, "for-each-ref", "--count=1", "--format=%(objectname)").Output()
	return err == nil && strings.TrimSpace(string(out)) == ""
}

// defaultServicesDir is where stacks live on a bare-initialised server until
// the first pushed config.yml says otherwise.
const defaultServicesDir = "/srv"

// PendingConfig stands in for config.yml on a bare-initialised server that has
// received no push yet, so herald serve can run and wait for the first one. It
// is not validated: maintenance stops at the missing config.yml before it
// starts Caddy or deploys. ok is false once anything has been pushed.
func PendingConfig(ctx context.Context, dataDir string) (cfg *config.Config, ok bool) {
	if !NothingPushed(ctx, dataDir) {
		return nil, false
	}
	name, _ := os.Hostname()
	return &config.Config{Server: config.Server{Name: name, ServicesDir: defaultServicesDir, Port: 9483}}, true
}

func printBareCompletion(w io.Writer, opts BareOptions, remotes []string) {
	name, _ := os.Hostname()
	services := opts.ServicesDir
	if services == "" {
		services = defaultServicesDir
	}
	fmt.Fprintln(w, "Herald initialized successfully!\n\nOn your laptop, choose one remote (LAN or NetBird):")
	for _, command := range remotes {
		fmt.Fprintln(w, "  "+command)
	}
	fmt.Fprintf(w, "\nPush an existing server repo with config.yml:\n  git push %s HEAD:main\n", name)
	fmt.Fprintf(w, "\nOr start a new repo on your laptop:\n  mkdir server && cd server\n  git init -b main\n  cat > config.yml <<'EOF'\nserver:\n  name: %q\n  services_dir: %q\n  acme_email: ops@example.com\nEOF\n", name, services)
	fmt.Fprintln(w, "  # Replace acme_email with your email; add stacks to config.yml.")
	fmt.Fprintln(w, "  # Run one of the git remote add commands above here.")
	fmt.Fprintf(w, "  git add config.yml\n  git commit -m 'Configure server'\n  git push %s HEAD:main\n", name)
	fmt.Fprintln(w, "\nOn this server, start the daemon before pushing:\n  sudo systemctl enable --now herald\nCheck that a push landed:\n  herald status")
	fmt.Fprintf(w, "\nOnly on-server copies to add to backups:\n  Bare repo: %s\n  Age store: %s and %s (keep the key with the encrypted store)\n", filepath.Join(opts.DataDir, "server.git"), filepath.Join(opts.DataDir, "age.key"), filepath.Join(opts.DataDir, "secrets.age"))
}
