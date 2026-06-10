package caddy

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/runner"
)

//go:embed compose.yml.tmpl
var composeTemplate string

var composeTmpl = template.Must(template.New("compose").Parse(composeTemplate))

const (
	caddyNetwork       = "caddy"
	caddyContainerName = "herald-caddy"
)

// CaddyManager manages the Caddy reverse proxy lifecycle.
type CaddyManager struct {
	Config     *config.Config
	Logger     *slog.Logger
	HeraldPort int
}

// CaddyStatus holds the current state of Caddy and its proxied domains.
type CaddyStatus struct {
	Running bool
	Uptime  string
	Email   string
	Domains []ProxiedDomain
}

// ProxiedDomain represents a domain being proxied by Caddy.
type ProxiedDomain struct {
	Domain   string
	Upstream string
}

func (m *CaddyManager) composeFilePath() string {
	return filepath.Join(m.Config.Server.ServicesDir, "caddy", "compose.yml")
}

// EnsureNetwork creates the caddy Docker network if it doesn't exist.
func EnsureNetwork(ctx context.Context, logger *slog.Logger) error {
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", caddyNetwork)
	if err := cmd.Run(); err == nil {
		return nil
	}
	return runner.RunCmd(ctx, logger, "", "docker", "network", "create", caddyNetwork)
}

// NetworkExists reports whether the caddy Docker network exists. Read-only: unlike
// EnsureNetwork it never creates the network, so it is safe for diagnosis.
func NetworkExists(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "network", "inspect", caddyNetwork).Run() == nil
}

// EnsureNetwork is a method that delegates to the package-level EnsureNetwork function.
func (m *CaddyManager) EnsureNetwork(ctx context.Context) error {
	return EnsureNetwork(ctx, m.Logger)
}

// Start ensures the network exists, writes the compose file, and starts Caddy.
func (m *CaddyManager) Start(ctx context.Context) error {
	if err := m.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}

	composePath := m.composeFilePath()
	if err := os.MkdirAll(filepath.Dir(composePath), 0755); err != nil {
		return fmt.Errorf("creating caddy dir: %w", err)
	}

	content := generateComposeContent(m.Config.Server.AcmeEmail, m.Config.Server.DeployDomain, m.HeraldPort)
	if err := os.WriteFile(composePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}

	err := runner.RunCmd(ctx, m.Logger, "", "docker", "compose", "-f", composePath, "-p", "herald-caddy", "up", "-d")
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			return fmt.Errorf("ports 80/443 are in use. Stop the existing proxy (nginx-proxy?) before starting Caddy")
		}
		return err
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := m.IsRunning(ctx); running {
			break
		}
		time.Sleep(2 * time.Second)
	}

	m.Logger.Info("caddy started", "ports", ":80/:443", "acme_email", m.Config.Server.AcmeEmail)
	return nil
}

// Stop tears down the Caddy compose stack without removing the network or volumes.
func (m *CaddyManager) Stop(ctx context.Context) error {
	composePath := m.composeFilePath()
	if err := runner.RunCmd(ctx, m.Logger, "", "docker", "compose", "-f", composePath, "-p", "herald-caddy", "down"); err != nil {
		return err
	}
	m.Logger.Info("caddy stopped")
	return nil
}

// IsRunning reports whether the herald-caddy container is running.
func (m *CaddyManager) IsRunning(ctx context.Context) (bool, error) {
	out, err := runCmdOutput(ctx, "docker", "ps", "-q", "--filter", "name=herald-caddy", "--filter", "status=running")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Status returns the current Caddy state and proxied domain list.
func (m *CaddyManager) Status(ctx context.Context) (*CaddyStatus, error) {
	s := &CaddyStatus{Email: m.Config.Server.AcmeEmail}

	running, err := m.IsRunning(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking running state: %w", err)
	}
	s.Running = running

	if running {
		out, err := runCmdOutput(ctx, "docker", "inspect", "--format", "{{.State.StartedAt}}", caddyContainerName)
		if err == nil {
			s.Uptime = formatUptime(strings.TrimSpace(out))
		}
	}

	domains, err := m.listProxiedDomains(ctx)
	if err != nil {
		m.Logger.Warn("could not list proxied domains", "error", err)
	}
	s.Domains = domains

	return s, nil
}

func (m *CaddyManager) listProxiedDomains(ctx context.Context) ([]ProxiedDomain, error) {
	out, err := runCmdOutput(ctx, "docker", "ps", "--filter", "network=caddy", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(strings.TrimSpace(out))
	if len(ids) == 0 {
		return nil, nil
	}

	args := append([]string{"inspect"}, ids...)
	out, err = runCmdOutput(ctx, "docker", args...)
	if err != nil {
		return nil, err
	}

	type containerInfo struct {
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}

	var containers []containerInfo
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		return nil, fmt.Errorf("parsing docker inspect: %w", err)
	}

	var domains []ProxiedDomain
	for _, c := range containers {
		domain, ok := c.Config.Labels["caddy"]
		if !ok || domain == "" {
			continue
		}
		name := strings.TrimPrefix(c.Name, "/")
		domains = append(domains, ProxiedDomain{
			Domain:   domain,
			Upstream: parseCaddyUpstream(name, c.Config.Labels),
		})
	}
	return domains, nil
}

var upstreamsPortRe = regexp.MustCompile(`\{\{upstreams\s+(\d+)\}\}`)

func parseCaddyUpstream(containerName string, labels map[string]string) string {
	if to, ok := labels["caddy.reverse_proxy.to"]; ok {
		to = strings.Replace(to, "host.docker.internal:", "host:", 1)
		return to + " (herald)"
	}
	if rp, ok := labels["caddy.reverse_proxy"]; ok {
		if m := upstreamsPortRe.FindStringSubmatch(rp); len(m) >= 2 {
			return containerName + ":" + m[1]
		}
	}
	return containerName
}

func formatUptime(startedAt string) string {
	t, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		mins := int(d.Minutes())
		if mins <= 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}
}

type composeData struct {
	AcmeEmail    string
	DeployDomain string
	GatewayIP    string
	HeraldPort   int
}

func generateComposeContent(acmeEmail, deployDomain string, heraldPort int) string {
	var buf bytes.Buffer
	data := composeData{
		AcmeEmail:    acmeEmail,
		DeployDomain: deployDomain,
		GatewayIP:    getDockerGatewayIP(),
		HeraldPort:   heraldPort,
	}
	if err := composeTmpl.Execute(&buf, data); err != nil {
		// Template is embedded and tested — this should never fail.
		panic(fmt.Sprintf("caddy compose template: %v", err))
	}
	return buf.String()
}

// getDockerGatewayIP returns the gateway IP of the default Docker bridge network.
// This is the IP containers use to reach services on the host.
func getDockerGatewayIP() string {
	out, err := exec.Command("docker", "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").Output()
	if err == nil {
		ip := strings.TrimSpace(string(out))
		if ip != "" {
			return ip
		}
	}
	// Fallback: typical Docker bridge gateway
	return "172.17.0.1"
}

func runCmdOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
