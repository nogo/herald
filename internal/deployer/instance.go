package deployer

import (
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
)

// projectPrefix starts every compose project Herald owns, which is how orphans
// are told apart from containers Herald does not manage.
const projectPrefix = "herald-"

// Instance is one deployment of a stack on this server: its directory and its
// compose project. A production stack and each of its previews are separate
// instances. Instance owns the directory layout, so nothing else joins
// "repo", ".env" or "compose.override.yml" onto a deploy dir.
type Instance struct {
	Dir     string // deploy dir holding repo/, .env, secrets/, compose.override.yml
	Project string // docker compose project name
}

// StackInstance returns the production instance of the named stack.
func StackInstance(cfg *config.Config, name string) Instance {
	return Instance{
		Dir:     filepath.Join(cfg.Server.ServicesDir, name),
		Project: projectPrefix + name,
	}
}

// PreviewInstance returns the instance of the preview with the given ID.
func PreviewInstance(cfg *config.Config, id string) Instance {
	return Instance{
		Dir:     filepath.Join(cfg.Server.ServicesDir, "previews", id),
		Project: projectPrefix + "preview-" + id,
	}
}

// RepoDir is where the stack's source lives: a git clone for repo stacks, a
// symlink into the IaC repo for path stacks.
func (i Instance) RepoDir() string { return filepath.Join(i.Dir, "repo") }

// EnvFile is the generated .env: config file base merged with secrets.
func (i Instance) EnvFile() string { return filepath.Join(i.Dir, ".env") }

// OverrideFile is the generated compose override: caddy labels, networks, secrets.
func (i Instance) OverrideFile() string { return filepath.Join(i.Dir, "compose.override.yml") }

// InternalNetwork isolates the instance's services from every other instance.
func (i Instance) InternalNetwork() string { return i.Project + "-internal" }

// Exists reports whether the instance has ever been deployed.
func (i Instance) Exists() bool {
	_, err := os.Stat(i.Dir)
	return err == nil
}

// ComposeFile returns the absolute path of the stack's compose file in this
// instance. Repo stacks name it in config; path stacks use whichever standard
// compose file their directory contains.
func (i Instance) ComposeFile(stack config.Stack) (string, error) {
	if stack.Repo != "" {
		return resolveComposePath(stack.Compose, i.RepoDir()), nil
	}
	name, err := compose.FindComposeFile(i.RepoDir())
	if err != nil {
		return "", err
	}
	return filepath.Join(i.RepoDir(), name), nil
}

// composeContext returns the docker compose invocation for the instance. The
// generated .env and override are included only once they exist, so a stack
// whose deploy failed early can still be taken down.
func (i Instance) composeContext(composeFile string) compose.Context {
	c := compose.Context{
		ProjectName: i.Project,
		ComposeFile: composeFile,
		WorkDir:     i.RepoDir(),
	}
	if _, err := os.Stat(i.EnvFile()); err == nil {
		c.EnvFile = i.EnvFile()
	}
	if _, err := os.Stat(i.OverrideFile()); err == nil {
		c.OverrideFile = i.OverrideFile()
	}
	return c
}
