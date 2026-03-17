package status

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/preview"
)

// StatusCollector gathers live status from Docker, config, and state files.
type StatusCollector struct {
	Config  *config.Config
	DataDir string
	Logger  *slog.Logger
	Caddy   *caddy.CaddyManager
	Preview *preview.PreviewManager
}

// ServerStatus holds a complete snapshot of the server's runtime state.
type ServerStatus struct {
	ServerName      string          `json:"server_name"`
	Caddy           CaddyStatus     `json:"caddy"`
	Apps            []AppStatus     `json:"apps,omitempty"`
	Stacks          []StackStatus   `json:"stacks,omitempty"`
	Previews        []PreviewStatus `json:"previews,omitempty"`
	Webhooks        []WebhookStatus `json:"webhooks,omitempty"`
	WebhookSyncedAt time.Time       `json:"webhook_synced_at,omitzero"`
}

// CaddyStatus describes the current state of the Caddy reverse proxy.
type CaddyStatus struct {
	Running     bool   `json:"running"`
	Uptime      string `json:"uptime,omitzero"`
	Email       string `json:"email,omitzero"`
	DomainCount int    `json:"domain_count,omitzero"`
}

// AppStatus describes the runtime state of a configured app.
type AppStatus struct {
	Name            string `json:"name"`
	Domain          string `json:"domain"`
	Branch          string `json:"branch"`
	State           string `json:"state"` // "running", "degraded", "stopped", "not deployed", "error"
	ContainersUp    int    `json:"containers_up,omitzero"`
	ContainersTotal int    `json:"containers_total,omitzero"`
	LastCommit      string `json:"last_commit,omitzero"`
}

// StackStatus describes the runtime state of a configured stack.
type StackStatus struct {
	Name            string `json:"name"`
	Domain          string `json:"domain"`
	State           string `json:"state"`
	ContainersUp    int    `json:"containers_up,omitzero"`
	ContainersTotal int    `json:"containers_total,omitzero"`
}

// PreviewStatus describes an active preview deployment.
type PreviewStatus struct {
	AppName   string    `json:"app_name"`
	Domain    string    `json:"domain"`
	Branch    string    `json:"branch"`
	Age       string    `json:"age"`
	Commit    string    `json:"commit,omitzero"`
	CreatedAt time.Time `json:"created_at"`
}

// WebhookStatus describes the webhook registration state for a repo.
type WebhookStatus struct {
	Repo       string `json:"repo"`
	Registered bool   `json:"registered"`
	ID         int64  `json:"id,omitzero"`
	Unknown    bool   `json:"unknown,omitzero"` // true if webhooks.json doesn't exist or is unparseable
}

// WebhookEntry is a single repo entry in webhooks.json.
type WebhookEntry struct {
	ID         int64 `json:"id"`
	Registered bool  `json:"registered"`
}

// WebhookState is the full webhooks.json structure.
type WebhookState struct {
	SyncedAt time.Time               `json:"synced_at"`
	Repos    map[string]WebhookEntry `json:"repos"`
}

// WebhookStatePath returns the path to webhooks.json.
func WebhookStatePath(dataDir string) string {
	return filepath.Join(dataDir, "webhooks.json")
}

// SaveWebhookState writes the webhook state to disk atomically.
func SaveWebhookState(path string, state *WebhookState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling webhook state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing webhook state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("renaming webhook state: %w", err)
	}
	return nil
}

// Collect gathers live status from all sources concurrently.
// It completes within 10 seconds even with many services.
func (c *StatusCollector) Collect(ctx context.Context) (*ServerStatus, error) {
	s := &ServerStatus{
		ServerName: c.Config.Server.Name,
	}

	// Caddy status.
	if c.Caddy != nil {
		cs, err := c.Caddy.Status(ctx)
		if err != nil {
			c.Logger.Warn("could not get caddy status", "error", err)
		} else {
			s.Caddy = CaddyStatus{
				Running:     cs.Running,
				Uptime:      cs.Uptime,
				Email:       cs.Email,
				DomainCount: len(cs.Domains),
			}
		}
	}

	// App statuses (concurrent).
	appNames := slices.Sorted(maps.Keys(c.Config.Apps))
	appStatuses := make([]AppStatus, len(appNames))
	var wg sync.WaitGroup
	for i, name := range appNames {
		wg.Go(func() {
			appStatuses[i] = c.collectAppStatus(ctx, name, c.Config.Apps[name])
		})
	}
	wg.Wait()
	s.Apps = appStatuses

	// Stack statuses (concurrent).
	stackNames := slices.Sorted(maps.Keys(c.Config.Services))
	stackStatuses := make([]StackStatus, len(stackNames))
	for i, name := range stackNames {
		wg.Go(func() {
			stackStatuses[i] = c.collectStackStatus(ctx, name, c.Config.Services[name])
		})
	}
	wg.Wait()
	s.Stacks = stackStatuses

	// Preview statuses.
	if c.Preview != nil {
		previews, err := c.Preview.List()
		if err != nil {
			c.Logger.Warn("could not list previews", "error", err)
		} else {
			ps := make([]PreviewStatus, len(previews))
			for i, p := range previews {
				ps[i] = PreviewStatus{
					AppName:   p.AppName,
					Domain:    p.Domain,
					Branch:    p.Branch,
					Age:       formatAge(time.Since(p.CreatedAt)),
					Commit:    p.Commit,
					CreatedAt: p.CreatedAt,
				}
			}
			s.Previews = ps
		}
	}

	// Webhook statuses.
	s.Webhooks, s.WebhookSyncedAt = c.collectWebhookStatuses()

	return s, nil
}

func (c *StatusCollector) collectAppStatus(ctx context.Context, name string, app config.App) AppStatus {
	s := AppStatus{
		Name:   name,
		Domain: app.Domain,
		Branch: app.Branch,
	}

	appDir := filepath.Join(c.Config.Server.StacksDir, "apps", name)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		s.State = "not deployed"
		return s
	}

	repoDir := filepath.Join(appDir, "repo")
	if commit, err := readGitHead(ctx, repoDir); err == nil {
		s.LastCommit = commit
	}

	up, total, state, err := queryDockerCompose(ctx, "herald-"+name)
	if err != nil {
		c.Logger.Warn("docker compose ps failed", "app", name, "error", err)
		s.State = "error"
		return s
	}
	s.ContainersUp = up
	s.ContainersTotal = total
	s.State = state
	return s
}

func (c *StatusCollector) collectStackStatus(ctx context.Context, name string, stack config.Service) StackStatus {
	s := StackStatus{
		Name:   name,
		Domain: stack.Domain,
	}

	up, total, state, err := queryDockerCompose(ctx, "herald-stack-"+name)
	if err != nil {
		c.Logger.Warn("docker compose ps failed", "stack", name, "error", err)
		s.State = "error"
		return s
	}
	s.ContainersUp = up
	s.ContainersTotal = total
	s.State = state
	return s
}

func (c *StatusCollector) collectWebhookStatuses() ([]WebhookStatus, time.Time) {
	repos := uniqueRepos(c.Config)
	wsPath := WebhookStatePath(c.DataDir)

	data, err := os.ReadFile(wsPath)
	if err != nil {
		// File not found — show "unknown" for all repos.
		statuses := make([]WebhookStatus, len(repos))
		for i, repo := range repos {
			statuses[i] = WebhookStatus{Repo: repo, Unknown: true}
		}
		return statuses, time.Time{}
	}

	var state WebhookState
	if err := json.Unmarshal(data, &state); err != nil {
		c.Logger.Warn("could not parse webhooks.json", "error", err)
		statuses := make([]WebhookStatus, len(repos))
		for i, repo := range repos {
			statuses[i] = WebhookStatus{Repo: repo, Unknown: true}
		}
		return statuses, time.Time{}
	}

	statuses := make([]WebhookStatus, len(repos))
	for i, repo := range repos {
		entry, ok := state.Repos[repo]
		statuses[i] = WebhookStatus{
			Repo:       repo,
			Registered: ok && entry.Registered,
			ID:         entry.ID,
		}
	}
	return statuses, state.SyncedAt
}

// containerPS holds the container state fields from `docker compose ps --format json`.
type containerPS struct {
	State string `json:"State"`
}

// queryDockerCompose runs `docker compose -p <project> ps --format json` and
// returns (running_count, total_count, state_string, error).
// On Docker error or missing project, returns (0, 0, "stopped", nil).
func queryDockerCompose(ctx context.Context, project string) (up, total int, state string, err error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "ps", "--format", "json")
	out, runErr := cmd.Output()
	if runErr != nil {
		// Docker not accessible or project not found → treat as stopped.
		return 0, 0, "stopped", nil
	}

	containers, parseErr := parseComposePSOutput(out)
	if parseErr != nil {
		return 0, 0, "error", parseErr
	}

	up, total, state = computeState(containers)
	return up, total, state, nil
}

// parseComposePSOutput parses `docker compose ps --format json` output.
// Docker Compose v2 outputs NDJSON (one object per line), but older versions
// may output a JSON array.
func parseComposePSOutput(out []byte) ([]containerPS, error) {
	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" || output == "null" {
		return nil, nil
	}

	// Try JSON array first.
	if strings.HasPrefix(output, "[") {
		var containers []containerPS
		if err := json.Unmarshal([]byte(output), &containers); err != nil {
			return nil, fmt.Errorf("parsing docker compose ps output: %w", err)
		}
		return containers, nil
	}

	// NDJSON: one JSON object per line.
	var containers []containerPS
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c containerPS
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue // skip unparseable lines
		}
		containers = append(containers, c)
	}
	return containers, nil
}

// computeState derives the state string from a list of containers.
func computeState(containers []containerPS) (up, total int, state string) {
	total = len(containers)
	for _, c := range containers {
		if strings.EqualFold(c.State, "running") {
			up++
		}
	}
	switch {
	case total == 0:
		state = "stopped"
	case up == total:
		state = "running"
	case up == 0:
		state = "stopped"
	default:
		state = "degraded"
	}
	return up, total, state
}

// readGitHead reads the short commit hash from a git repository.
func readGitHead(ctx context.Context, repoDir string) (string, error) {
	if _, err := os.Stat(repoDir); err != nil {
		return "", err
	}
	out, err := githelper.CmdWithAuth(ctx, "", repoDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// uniqueRepos returns sorted, deduplicated repos from config.Apps.
func uniqueRepos(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	for _, app := range cfg.Apps {
		seen[app.Repo] = struct{}{}
	}
	return slices.Sorted(maps.Keys(seen))
}

// formatAge formats a duration as a short human-readable age string.
func formatAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d"
		}
		return fmt.Sprintf("%dd", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1h"
		}
		return fmt.Sprintf("%dh", hours)
	default:
		mins := int(d.Minutes())
		if mins <= 1 {
			return "1m"
		}
		return fmt.Sprintf("%dm", mins)
	}
}
