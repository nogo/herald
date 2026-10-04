package deployer

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
)

// projectPrefix starts every compose project Herald owns, which is how orphans
// are told apart from containers Herald does not manage.
const projectPrefix = "herald-"

// Deploy stamps, written into the deploy dir after a successful deploy.
const (
	refStamp    = "deployed_ref"    // "<ref>@<commit>"; path stacks use "path@<IaC commit>"
	configStamp = "deployed_config" // config.Stack.Hash of the deployed config
)

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

// StackNameOf returns the production stack a compose project belongs to. It
// reports false for projects Herald did not create for a production stack:
// previews and everything outside the herald- prefix. Herald's own caddy
// project carries the prefix too; see caddy.ProjectName.
func StackNameOf(project string) (string, bool) {
	name, ok := strings.CutPrefix(project, projectPrefix)
	if !ok || name == "" || strings.HasPrefix(name, "preview-") {
		return "", false
	}
	return name, true
}

// RepoDir is where the stack's source lives: a git clone for repo stacks, a
// symlink into the IaC repo for path stacks.
func (i Instance) RepoDir() string { return filepath.Join(i.Dir, "repo") }

// EnvFile is the generated .env: config file base merged with secrets.
func (i Instance) EnvFile() string { return filepath.Join(i.Dir, ".env") }

// SecretsDir holds one file per docker secret, mounted by the override.
func (i Instance) SecretsDir() string { return filepath.Join(i.Dir, "secrets") }

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

// ComposeArgs returns the docker compose arguments that address this instance,
// ready for a subcommand to be appended. Run them from RepoDir.
func (i Instance) ComposeArgs(composeFile string) []string {
	return i.composeContext(composeFile).BaseArgs()
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

// DeployedRef returns the raw ref stamp of the last successful deploy
// ("main@abc1234", "path@def5678"), or "" if there is none.
func (i Instance) DeployedRef() string {
	data, err := os.ReadFile(filepath.Join(i.Dir, refStamp))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// DeployedIaCCommit returns the IaC commit a path stack was last deployed from,
// or "" if the instance has no record or is not a path stack.
func (i Instance) DeployedIaCCommit() string {
	commit, _ := strings.CutPrefix(i.DeployedRef(), "path@")
	if commit == i.DeployedRef() {
		return ""
	}
	return commit
}

// ConfigDrifted reports whether config.yml changed the stack since its last
// deploy. A stack deployed before Herald recorded fingerprints has no stamp; that
// is reported as no drift, so an upgrade does not flag every stack at once.
func (i Instance) ConfigDrifted(stack config.Stack) bool {
	data, err := os.ReadFile(filepath.Join(i.Dir, configStamp))
	if err != nil {
		return false
	}
	recorded := strings.TrimSpace(string(data))
	return recorded != "" && recorded != stack.Hash()
}

// The stamps are best-effort: a deploy that succeeded is not failed because its
// record could not be written; the next pass just sees an older record.
func (i Instance) recordRef(ref, commit string) {
	_ = os.WriteFile(filepath.Join(i.Dir, refStamp), []byte(ref+"@"+commit), 0644)
}

func (i Instance) recordConfig(stack config.Stack) {
	_ = os.WriteFile(filepath.Join(i.Dir, configStamp), []byte(stack.Hash()), 0644)
}
