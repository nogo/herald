package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/config"
)

// Context holds the resolved paths needed to run docker compose commands
// for an app, service, or preview.
type Context struct {
	ProjectName  string
	ComposeFile  string
	OverrideFile string // empty if file doesn't exist
	EnvFile      string // empty if file doesn't exist
	WorkDir      string
}

// BaseArgs returns the common docker compose arguments:
// [compose, --project-name, X, --env-file, X, -f, X, -f, X]
// Optional flags (--env-file, second -f) are omitted when their paths are empty.
func (c Context) BaseArgs() []string {
	args := []string{
		"compose",
		"--project-name", c.ProjectName,
	}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	args = append(args, "-f", c.ComposeFile)
	if c.OverrideFile != "" {
		args = append(args, "-f", c.OverrideFile)
	}
	return args
}

// ResolveApp builds a Context for the named app.
func ResolveApp(cfg *config.Config, appName string) (*Context, error) {
	app, ok := cfg.Apps[appName]
	if !ok {
		return nil, fmt.Errorf("app %q not found in config", appName)
	}

	appDir := filepath.Join(cfg.Server.ServicesDir, "apps", appName)
	repoDir := filepath.Join(appDir, "repo")

	composeFile := app.Compose
	if !filepath.IsAbs(composeFile) {
		composeFile = filepath.Join(repoDir, composeFile)
	}

	ctx := &Context{
		ProjectName: "herald-" + appName,
		ComposeFile: composeFile,
		WorkDir:     repoDir,
	}

	if overrideFile := filepath.Join(appDir, "compose.override.yml"); fileExists(overrideFile) {
		ctx.OverrideFile = overrideFile
	}
	if envFile := filepath.Join(appDir, ".env"); fileExists(envFile) {
		ctx.EnvFile = envFile
	}

	return ctx, nil
}

// ResolveService builds a Context for the named service.
func ResolveService(cfg *config.Config, stackName string) (*Context, error) {
	stack, ok := cfg.Services[stackName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in config", stackName)
	}

	deployDir := filepath.Join(cfg.Server.ServicesDir, "services", stackName)
	repoLink := filepath.Join(deployDir, "repo")

	composeName, err := FindComposeFile(repoLink)
	if err != nil {
		return nil, err
	}

	ctx := &Context{
		ProjectName: "herald-svc-" + stackName,
		ComposeFile: filepath.Join(repoLink, composeName),
		WorkDir:     deployDir,
	}

	if overrideFile := filepath.Join(deployDir, "compose.override.yml"); fileExists(overrideFile) {
		ctx.OverrideFile = overrideFile
	}
	if stack.EnvFile != "" {
		if envFile := filepath.Join(deployDir, stack.EnvFile); fileExists(envFile) {
			ctx.EnvFile = envFile
		}
	}

	return ctx, nil
}

// previewState mirrors the preview state file structure (avoids circular import).
type previewState struct {
	Previews []previewEntry `json:"previews"`
}

type previewEntry struct {
	ID             string `json:"id"`
	Directory      string `json:"directory"`
	ComposeProject string `json:"compose_project"`
	ComposeFile    string `json:"compose_file"`
}

// ResolvePreview builds a Context for the named preview.
// dataDir is the herald data directory (e.g. /etc/herald) where previews.json lives.
func ResolvePreview(cfg *config.Config, dataDir, previewID string) (*Context, error) {
	statePath := filepath.Join(dataDir, "previews.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("reading preview state: %w", err)
	}

	var state previewState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing preview state: %w", err)
	}

	for _, p := range state.Previews {
		if p.ID == previewID {
			repoDir := filepath.Join(p.Directory, "repo")
			composeFile := p.ComposeFile
			if !filepath.IsAbs(composeFile) {
				composeFile = filepath.Join(repoDir, composeFile)
			}

			ctx := &Context{
				ProjectName: p.ComposeProject,
				ComposeFile: composeFile,
				WorkDir:     repoDir,
			}

			if overrideFile := filepath.Join(p.Directory, "compose.override.yml"); fileExists(overrideFile) {
				ctx.OverrideFile = overrideFile
			}
			if envFile := filepath.Join(p.Directory, ".env"); fileExists(envFile) {
				ctx.EnvFile = envFile
			}

			return ctx, nil
		}
	}

	return nil, fmt.Errorf("preview %q not found", previewID)
}

// Resolve tries to find a Context for the given name, checking apps first,
// then services, then previews. Returns the context and entity type.
func Resolve(cfg *config.Config, dataDir, name string) (*Context, string, error) {
	if _, ok := cfg.Apps[name]; ok {
		ctx, err := ResolveApp(cfg, name)
		return ctx, "app", err
	}
	if _, ok := cfg.Services[name]; ok {
		ctx, err := ResolveService(cfg, name)
		return ctx, "service", err
	}

	// Try as preview ID.
	ctx, err := ResolvePreview(cfg, dataDir, name)
	if err == nil {
		return ctx, "preview", nil
	}

	return nil, "", fmt.Errorf("%q not found as app, service, or preview", name)
}

// FindComposeFile returns the first compose filename found in dir.
func FindComposeFile(dir string) (string, error) {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if fileExists(filepath.Join(dir, name)) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", dir)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
