package preview

import (
	"context"
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

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/runner"
	"github.com/nogo/herald/internal/secrets"
)

const maxPreviewsPerApp = 10

// PreviewManager manages preview deployment lifecycle.
type PreviewManager struct {
	Config  *config.Config
	Secrets *secrets.Store
	DataDir string
	Logger  *slog.Logger

	mu sync.Mutex // serialises state file reads and writes
}

// SubdomainFromBranch derives the preview subdomain for a branch name.
// The previewDomain must contain a wildcard, e.g. "*.preview.basalt.solutions".
func SubdomainFromBranch(branch, previewDomain string) string {
	return strings.Replace(previewDomain, "*", branchSlug(branch), 1)
}

// branchSlug converts a branch name into a DNS-safe label (max 63 chars).
func branchSlug(branch string) string {
	branch = strings.TrimPrefix(branch, "refs/heads/")

	// Join path segments with "-" using iterator pattern.
	var parts []string
	for part := range strings.SplitSeq(branch, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	branch = strings.Join(parts, "-")

	// Replace non-alphanumeric (except "-") with "-".
	var b strings.Builder
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	slug := strings.ToLower(b.String())
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return strings.Trim(slug, "-")
}

// makeID generates a preview ID from the app name and branch.
func makeID(appName, branch string) string {
	return appName + "-" + branchSlug(branch)
}

// Deploy creates or updates a preview for the given app and branch.
func (m *PreviewManager) Deploy(ctx context.Context, appName, branch, commit string) error {
	app, ok := m.Config.Apps[appName]
	if !ok {
		return fmt.Errorf("app %q not found in config", appName)
	}
	if app.Preview == nil || !app.Preview.Enabled {
		return fmt.Errorf("app %q does not have preview enabled", appName)
	}

	id := makeID(appName, branch)
	domain := SubdomainFromBranch(branch, app.Preview.Domain)
	previewDir := m.previewDir(id)
	composeProject := "herald-preview-" + id

	m.mu.Lock()
	state, err := loadState(statePath(m.DataDir))
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("loading state: %w", err)
	}

	// Count existing previews for this app (excluding an update to this id).
	existingCount := 0
	isUpdate := false
	for _, p := range state.Previews {
		if p.AppName == appName {
			if p.ID == id {
				isUpdate = true
			} else {
				existingCount++
			}
		}
	}
	m.mu.Unlock()

	if !isUpdate && existingCount >= maxPreviewsPerApp {
		return fmt.Errorf("max previews (%d) reached for app %q", maxPreviewsPerApp, appName)
	}

	m.Logger.Info("preview deploy started", "id", id, "domain", domain)
	start := time.Now()

	if err := os.MkdirAll(previewDir, 0755); err != nil {
		return fmt.Errorf("creating preview dir: %w", err)
	}

	if err := m.gitSync(ctx, previewDir, app, branch); err != nil {
		return fmt.Errorf("git: %w", err)
	}

	envVars, dockerSecrets, err := m.Secrets.Resolve(app.Secrets)
	if err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}

	previewRoot, err := os.OpenRoot(previewDir)
	if err != nil {
		return fmt.Errorf("opening preview root: %w", err)
	}
	defer previewRoot.Close()

	if err := compose.WriteEnvFile(previewRoot, envVars); err != nil {
		return fmt.Errorf("writing .env: %w", err)
	}

	if len(dockerSecrets) > 0 {
		secretsDir := filepath.Join(previewDir, "secrets")
		if err := os.MkdirAll(secretsDir, 0700); err != nil {
			return fmt.Errorf("creating secrets dir: %w", err)
		}
		secretsRoot, err := previewRoot.OpenRoot("secrets")
		if err != nil {
			return fmt.Errorf("opening secrets root: %w", err)
		}
		defer secretsRoot.Close()
		for name, val := range dockerSecrets {
			if err := compose.WriteSecret(secretsRoot, name, val); err != nil {
				return fmt.Errorf("writing secret %q: %w", name, err)
			}
		}
	}

	repoDir := filepath.Join(previewDir, "repo")
	if err := m.generateOverride(previewRoot, previewDir, appName, id, domain, app, repoDir, dockerSecrets); err != nil {
		return fmt.Errorf("generating override: %w", err)
	}

	if err := caddy.EnsureNetwork(ctx, m.Logger); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}

	if err := m.runCompose(ctx, previewDir, composeProject, app.Compose); err != nil {
		return fmt.Errorf("compose: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, err = loadState(statePath(m.DataDir))
	if err != nil {
		return fmt.Errorf("reloading state: %w", err)
	}

	if isUpdate {
		for i, p := range state.Previews {
			if p.ID == id {
				state.Previews[i].Commit = commit
				break
			}
		}
	} else {
		state.Previews = append(state.Previews, PreviewInfo{
			ID:             id,
			AppName:        appName,
			Branch:         branch,
			Domain:         domain,
			Directory:      previewDir,
			ComposeProject: composeProject,
			ComposeFile:    app.Compose,
			CreatedAt:      time.Now().UTC(),
			Commit:         commit,
		})
	}

	if err := saveState(statePath(m.DataDir), state); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	m.Logger.Info("preview deployed", "id", id, "domain", domain, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// Teardown removes the preview for the given app and branch.
func (m *PreviewManager) Teardown(ctx context.Context, appName, branch string) error {
	return m.Remove(ctx, makeID(appName, branch))
}

// List returns all active previews.
func (m *PreviewManager) List() ([]PreviewInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := loadState(statePath(m.DataDir))
	if err != nil {
		return nil, err
	}
	return state.Previews, nil
}

// Remove tears down the preview with the given ID.
func (m *PreviewManager) Remove(ctx context.Context, previewID string) error {
	m.mu.Lock()
	state, err := loadState(statePath(m.DataDir))
	if err != nil {
		m.mu.Unlock()
		return err
	}

	var found *PreviewInfo
	for _, p := range state.Previews {
		if p.ID == previewID {
			pp := p
			found = &pp
			break
		}
	}
	m.mu.Unlock()

	if found == nil {
		return fmt.Errorf("preview %q not found", previewID)
	}

	if err := m.runComposeDown(ctx, found.Directory, found.ComposeProject, found.ComposeFile); err != nil {
		m.Logger.Warn("compose down failed", "id", previewID, "error", err)
	}

	// Prune images tagged with this preview.
	pruneCmd := exec.CommandContext(ctx, "docker", "image", "prune", "-f",
		"--filter", "label=com.herald.preview="+previewID)
	_ = pruneCmd.Run()

	if err := os.RemoveAll(found.Directory); err != nil {
		m.Logger.Warn("removing preview dir failed", "id", previewID, "error", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	state, err = loadState(statePath(m.DataDir))
	if err != nil {
		return fmt.Errorf("reloading state: %w", err)
	}
	state.Previews = slices.DeleteFunc(state.Previews, func(p PreviewInfo) bool {
		return p.ID == previewID
	})
	if err := saveState(statePath(m.DataDir), state); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	m.Logger.Info("preview removed", "id", previewID)
	return nil
}

// Cleanup removes previews whose branches no longer exist on the remote.
func (m *PreviewManager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	state, err := loadState(statePath(m.DataDir))
	m.mu.Unlock()
	if err != nil {
		return err
	}

	var removed []string
	for _, p := range state.Previews {
		app, ok := m.Config.Apps[p.AppName]
		if !ok {
			m.Logger.Warn("preview references unknown app, removing", "id", p.ID)
			if err := m.Remove(ctx, p.ID); err != nil {
				m.Logger.Error("cleanup failed", "id", p.ID, "error", err)
			} else {
				removed = append(removed, p.ID)
			}
			continue
		}

		exists, err := m.branchExists(ctx, app, p.Branch)
		if err != nil {
			m.Logger.Warn("could not check branch, skipping", "id", p.ID, "error", err)
			continue
		}
		if !exists {
			if err := m.Remove(ctx, p.ID); err != nil {
				m.Logger.Error("cleanup failed", "id", p.ID, "error", err)
			} else {
				removed = append(removed, p.ID)
			}
		}
	}

	if len(removed) > 0 {
		m.Logger.Info("preview cleanup complete", "removed", removed)
	} else {
		m.Logger.Info("preview cleanup: nothing to remove")
	}
	return nil
}

// previewDir returns the directory for a preview deployment.
func (m *PreviewManager) previewDir(id string) string {
	return filepath.Join(m.Config.Server.ServicesDir, "previews", id)
}

// branchExists checks whether the branch exists on the remote.
func (m *PreviewManager) branchExists(ctx context.Context, app config.App, branch string) (bool, error) {
	cmd := git.CmdWithAuth(ctx, m.Config.Server.GithubToken, "", "ls-remote", "--heads", git.RepoURL(app.Repo), branch)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// --- Deploy helpers ---

func (m *PreviewManager) gitSync(ctx context.Context, previewDir string, app config.App, branch string) error {
	repoDir := filepath.Join(previewDir, "repo")
	return git.CloneOrFetch(ctx, m.Config.Server.GithubToken, repoDir, git.RepoURL(app.Repo), branch)
}

func (m *PreviewManager) runCompose(ctx context.Context, previewDir, composeProject, composeFile string) error {
	repoDir := filepath.Join(previewDir, "repo")
	overrideFile := filepath.Join(previewDir, "compose.override.yml")
	m.Logger.Info("compose up", "project", composeProject)
	return runner.RunCmd(ctx, m.Logger, repoDir,
		"docker", "compose",
		"--project-name", composeProject,
		"-f", composeFile,
		"-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans",
	)
}

func (m *PreviewManager) runComposeDown(ctx context.Context, previewDir, composeProject, composeFile string) error {
	repoDir := filepath.Join(previewDir, "repo")
	overrideFile := filepath.Join(previewDir, "compose.override.yml")
	m.Logger.Info("compose down", "project", composeProject)
	return runner.RunCmd(ctx, m.Logger, repoDir,
		"docker", "compose",
		"--project-name", composeProject,
		"-f", composeFile,
		"-f", overrideFile,
		"down", "--volumes", "--remove-orphans",
	)
}

func (m *PreviewManager) generateOverride(
	root *os.Root,
	previewDir, appName, previewID, domain string,
	app config.App,
	repoDir string,
	dockerSecrets map[string]string,
) error {
	serviceName, port, err := compose.DetectServiceInfo(filepath.Join(repoDir, app.Compose), appName, "3000")
	if err != nil {
		m.Logger.Warn("could not detect service info, using defaults", "app", appName, "error", err)
		serviceName = "app"
		port = "3000"
	}

	svc := compose.ServiceOverride{
		Labels: map[string]string{
			"caddy":               domain,
			"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %s}}", port),
			"com.herald.preview":  previewID,
		},
		Networks: []string{"caddy"},
	}

	if app.EnvFile != "" {
		svc.EnvFile = []string{app.EnvFile}
	}

	secretNames := slices.Sorted(maps.Keys(dockerSecrets))
	if len(secretNames) > 0 {
		svc.Secrets = secretNames
	}

	override := compose.Override{
		Services: map[string]compose.ServiceOverride{serviceName: svc},
		Networks: map[string]compose.NetworkDef{"caddy": {External: true}},
	}

	if len(secretNames) > 0 {
		override.Secrets = make(map[string]compose.SecretFileDef, len(secretNames))
		for _, name := range secretNames {
			override.Secrets[name] = compose.SecretFileDef{
				File: filepath.Join(previewDir, "secrets", name),
			}
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return fmt.Errorf("marshaling override: %w", err)
	}

	if app.Override != "" {
		var baseMap, overlayMap map[string]any
		if err := yaml.Unmarshal(data, &baseMap); err != nil {
			return fmt.Errorf("re-parsing generated override: %w", err)
		}
		if err := yaml.Unmarshal([]byte(app.Override), &overlayMap); err != nil {
			return fmt.Errorf("parsing app override YAML: %w", err)
		}
		merged := compose.DeepMerge(baseMap, overlayMap)
		data, err = yaml.Marshal(merged)
		if err != nil {
			return fmt.Errorf("marshaling merged override: %w", err)
		}
	}

	f, err := root.OpenFile("compose.override.yml", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
