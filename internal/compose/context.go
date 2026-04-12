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

// ResolveStack builds a Context for the named stack.
// Deploy dir: <servicesDir>/<stackName> (flat, no apps/ or services/ subdirectory).
// Project name: herald-<stackName>.
func ResolveStack(cfg *config.Config, stackName string) (*Context, error) {
	stack, ok := cfg.Stacks[stackName]
	if !ok {
		return nil, fmt.Errorf("stack %q not found in config", stackName)
	}

	deployDir := filepath.Join(cfg.Server.ServicesDir, stackName)
	repoDir := filepath.Join(deployDir, "repo")

	var composeFile string
	if stack.Repo != "" {
		cf := stack.Compose
		if !filepath.IsAbs(cf) {
			cf = filepath.Join(repoDir, cf)
		}
		composeFile = cf
	} else {
		composeName, err := FindComposeFile(repoDir)
		if err != nil {
			return nil, err
		}
		composeFile = filepath.Join(repoDir, composeName)
	}

	ctx := &Context{
		ProjectName: "herald-" + stackName,
		ComposeFile: composeFile,
		WorkDir:     repoDir,
	}

	if overrideFile := filepath.Join(deployDir, "compose.override.yml"); fileExists(overrideFile) {
		ctx.OverrideFile = overrideFile
	}
	if envFile := filepath.Join(deployDir, ".env"); fileExists(envFile) {
		ctx.EnvFile = envFile
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

// Resolve tries to find a Context for the given name, checking stacks first,
// then previews. Returns the context and entity type.
func Resolve(cfg *config.Config, dataDir, name string) (*Context, string, error) {
	if _, ok := cfg.Stacks[name]; ok {
		ctx, err := ResolveStack(cfg, name)
		return ctx, "stack", err
	}

	// Try as preview ID.
	ctx, err := ResolvePreview(cfg, dataDir, name)
	if err == nil {
		return ctx, "preview", nil
	}

	return nil, "", fmt.Errorf("%q not found as stack or preview", name)
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
