package preview

import (
	"context"
	"fmt"
	"log/slog"
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
	"github.com/nogo/herald/internal/deployer"
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
	app, ok := m.Config.Stacks[appName]
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

	if err := deployer.WriteEnvFile(filepath.Join(previewDir, ".env"), envVars); err != nil {
		return fmt.Errorf("writing .env: %w", err)
	}

	if len(dockerSecrets) > 0 {
		if err := deployer.WriteDockerSecrets(filepath.Join(previewDir, "secrets"), dockerSecrets); err != nil {
			return fmt.Errorf("writing docker secrets: %w", err)
		}
	}

	repoDir := filepath.Join(previewDir, "repo")
	envFilePaths := []string{filepath.Join(previewDir, ".env")}
	if app.EnvFile != "" {
		envFilePaths = append(envFilePaths, app.EnvFile)
	}
	composeFile := app.Compose
	if !filepath.IsAbs(composeFile) {
		composeFile = filepath.Join(repoDir, composeFile)
	}
	internalNet := "herald-preview-" + id + "-internal"

	overrideData, err := deployer.GenerateOverride(deployer.OverrideParams{
		DeployDir:      previewDir,
		StackName:      appName,
		Domain:         domain,
		ComposeFile:    composeFile,
		EnvFilePaths:   envFilePaths,
		DockerSecrets:  dockerSecrets,
		DefaultPort:    "3000",
		InternalNet:    internalNet,
		InlineOverride: app.Override,
	})
	if err != nil {
		return fmt.Errorf("generating override: %w", err)
	}

	// Merge the preview-specific label into the override.
	var parsedSvcs struct {
		Services map[string]any `yaml:"services"`
	}
	if parseErr := yaml.Unmarshal(overrideData, &parsedSvcs); parseErr == nil {
		for svcName := range parsedSvcs.Services {
			fragment := fmt.Sprintf("services:\n  %s:\n    labels:\n      com.herald.preview: %s\n", svcName, id)
			if merged, mergeErr := compose.DeepMergeYAML(overrideData, []byte(fragment)); mergeErr == nil {
				overrideData = merged
			}
			break
		}
	}

	if err := os.WriteFile(filepath.Join(previewDir, "compose.override.yml"), overrideData, 0644); err != nil {
		return fmt.Errorf("writing override: %w", err)
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
		app, ok := m.Config.Stacks[p.AppName]
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

func (m *PreviewManager) composeContext(previewDir, composeProject, composeFile string) compose.Context {
	repoDir := filepath.Join(previewDir, "repo")
	if !filepath.IsAbs(composeFile) {
		composeFile = filepath.Join(repoDir, composeFile)
	}
	return compose.Context{
		ProjectName:  composeProject,
		ComposeFile:  composeFile,
		OverrideFile: filepath.Join(previewDir, "compose.override.yml"),
		EnvFile:      filepath.Join(previewDir, ".env"),
		WorkDir:      repoDir,
	}
}

func (m *PreviewManager) runCompose(ctx context.Context, previewDir, composeProject, composeFile string) error {
	cctx := m.composeContext(previewDir, composeProject, composeFile)
	m.Logger.Info("compose up", "project", composeProject)
	args := cctx.BaseArgs()
	args = append(args, "up", "-d", "--build", "--remove-orphans")
	return runner.RunCmd(ctx, m.Logger, cctx.WorkDir, "docker", args...)
}

func (m *PreviewManager) runComposeDown(ctx context.Context, previewDir, composeProject, composeFile string) error {
	cctx := m.composeContext(previewDir, composeProject, composeFile)
	m.Logger.Info("compose down", "project", composeProject)
	args := cctx.BaseArgs()
	args = append(args, "down", "--volumes", "--remove-orphans")
	return runner.RunCmd(ctx, m.Logger, cctx.WorkDir, "docker", args...)
}
