package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/config"
)

// Context holds the resolved paths needed to run docker compose commands
// for a stack or preview.
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
