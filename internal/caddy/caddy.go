package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nogo/herald/internal/config"
)

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
	return filepath.Join(m.Config.Server.StacksDir, "caddy", "compose.yml")
}

// EnsureNetwork creates the caddy Docker network if it doesn't exist.
func (m *CaddyManager) EnsureNetwork(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", caddyNetwork)
	if err := cmd.Run(); err == nil {
		return nil
	}
	return runCmd(ctx, m.Logger, "docker", "network", "create", caddyNetwork)
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

	err := runCmd(ctx, m.Logger, "docker", "compose", "-f", composePath, "-p", "herald-caddy", "up", "-d")
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

	fmt.Printf("caddy started on :80/:443, ACME email: %s\n", m.Config.Server.AcmeEmail)
	return nil
}

// Stop tears down the Caddy compose stack without removing the network or volumes.
func (m *CaddyManager) Stop(ctx context.Context) error {
	composePath := m.composeFilePath()
	if err := runCmd(ctx, m.Logger, "docker", "compose", "-f", composePath, "-p", "herald-caddy", "down"); err != nil {
		return err
	}
	fmt.Println("caddy stopped")
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

func generateComposeContent(acmeEmail, deployDomain string, heraldPort int) string {
	return fmt.Sprintf(`services:
  caddy:
    image: lucaslorentz/caddy-docker-proxy:2.9
    container_name: herald-caddy
    restart: always
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
    environment:
      - CADDY_INGRESS_NETWORKS=caddy
    networks:
      - caddy
    labels:
      caddy.email: "%s"

  herald-proxy:
    image: alpine:3
    container_name: herald-proxy-label
    restart: always
    command: ["sleep", "infinity"]
    networks:
      - caddy
    labels:
      caddy: "%s"
      caddy.reverse_proxy: "{{upstreams}}"
      caddy.reverse_proxy.to: "host.docker.internal:%d"
    extra_hosts:
      - "host.docker.internal:host-gateway"

volumes:
  caddy_data:
  caddy_config:

networks:
  caddy:
    external: true
`, acmeEmail, deployDomain, heraldPort)
}

func runCmd(ctx context.Context, logger *slog.Logger, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	for line := range strings.Lines(stdout.String()) {
		line = strings.TrimRight(line, "\n\r")
		if line != "" {
			logger.Info(line, "cmd", name)
		}
	}
	if stderr.Len() > 0 {
		for line := range strings.Lines(stderr.String()) {
			line = strings.TrimRight(line, "\n\r")
			if line != "" {
				logger.Warn(line, "cmd", name)
			}
		}
	}
	if runErr != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return fmt.Errorf("%s: %w: %s", name, runErr, stderrStr)
		}
		return fmt.Errorf("%s: %w", name, runErr)
	}
	return nil
}

func runCmdOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
