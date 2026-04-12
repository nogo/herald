package deployer

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
	"sync/atomic"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/runner"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/ui"
)

// Deployer executes stack deploys.
type Deployer struct {
	Config  *config.Config
	Secrets *secrets.Store
	Logger  *slog.Logger
	DataDir string // path to herald data dir (e.g. /etc/herald); IaC repo lives at DataDir/repo
	UI      ui.UI  // optional; nil defaults to ui.Nop()

	appLocks sync.Map // string → *appLock
	wg       sync.WaitGroup
}

func (d *Deployer) ui() ui.UI {
	if d.UI != nil {
		return d.UI
	}
	return ui.Nop()
}

type appLock struct {
	mu    sync.Mutex
	count atomic.Int32 // goroutines running or waiting, max 2
}

func (d *Deployer) getAppLock(stackName string) *appLock {
	v, _ := d.appLocks.LoadOrStore(stackName, &appLock{})
	return v.(*appLock)
}

// DeployAsync dispatches a deploy in a goroutine with per-stack serialization.
// At most one deploy may be queued per stack; additional calls are dropped.
func (d *Deployer) DeployAsync(stackName, ref string) {
	lock := d.getAppLock(stackName)
	if lock.count.Add(1) > 2 {
		lock.count.Add(-1)
		d.Logger.Info("deploy already queued, dropping", "stack", stackName)
		return
	}

	d.wg.Go(func() {
		lock.mu.Lock()
		defer lock.mu.Unlock()
		defer lock.count.Add(-1)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := d.Deploy(ctx, stackName, ref); err != nil {
			d.Logger.Error("deploy failed", "stack", stackName, "error", err)
		}
	})
}

// Wait blocks until all in-progress deploys finish.
func (d *Deployer) Wait() {
	d.wg.Wait()
}

// effectiveRef returns the git ref to use for a deploy.
// override takes precedence; otherwise stack.Tag (as refs/tags/<tag>) or stack.Branch.
func effectiveRef(stack config.Stack, override string) string {
	if override != "" {
		return override
	}
	if stack.Tag != "" {
		return "refs/tags/" + stack.Tag
	}
	return stack.Branch
}

// Deploy executes a full deploy for the named stack.
func (d *Deployer) Deploy(ctx context.Context, stackName, ref string) error {
	stack, ok := d.Config.Stacks[stackName]
	if !ok {
		return fmt.Errorf("stack %q not found in config", stackName)
	}

	u := d.ui()
	start := time.Now()
	var deployErr error
	defer func() {
		u.Done(stackName, deployErr, time.Since(start))
	}()

	step := func(name string, fn func() error) error {
		u.Step(name)
		if err := fn(); err != nil {
			u.StepFail(err)
			return err
		}
		u.StepDone("")
		return nil
	}
	stepDetail := func(name string, fn func() (string, error)) error {
		u.Step(name)
		detail, err := fn()
		if err != nil {
			u.StepFail(err)
			return err
		}
		u.StepDone(detail)
		return nil
	}

	// Pre-flight: check for missing required secrets.
	deployErr = step("Preflight", func() error {
		missing, err := d.Secrets.MissingRequired(stack.Secrets)
		if err != nil {
			return fmt.Errorf("checking secrets: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing secrets (use `herald secret set <key>`): %s",
				strings.Join(missing, ", "))
		}
		return nil
	})
	if deployErr != nil {
		return deployErr
	}

	deployDir := filepath.Join(d.Config.Server.ServicesDir, stackName)
	d.Logger.Info("deploy started", "stack", stackName, "dir", deployDir)

	if err := os.MkdirAll(deployDir, 0755); err != nil {
		deployErr = fmt.Errorf("creating deploy dir: %w", err)
		return deployErr
	}

	repoDir := filepath.Join(deployDir, "repo")

	// Source resolution: git clone/fetch for repo stacks, symlink for path stacks.
	if stack.Repo != "" {
		gitRef := effectiveRef(stack, ref)
		deployErr = step(fmt.Sprintf("Git sync (%s)", gitRef), func() error {
			return d.gitSync(ctx, deployDir, stack, gitRef)
		})
	} else {
		deployErr = step("Symlink source", func() error {
			return d.symlinkSource(deployDir, stack)
		})
	}
	if deployErr != nil {
		return deployErr
	}

	// Verify env_file exists if configured.
	if stack.EnvFile != "" {
		if _, err := os.Stat(stack.EnvFile); err != nil {
			deployErr = fmt.Errorf("env_file %q not found", stack.EnvFile)
			return deployErr
		}
	}

	// Resolve secrets + write env + write docker secrets.
	var dockerSecrets map[string]string
	deployErr = stepDetail("Secrets", func() (string, error) {
		envVars, ds, err := d.Secrets.Resolve(stack.Secrets)
		if err != nil {
			return "", fmt.Errorf("resolving secrets: %w", err)
		}
		dockerSecrets = ds
		if len(envVars)+len(dockerSecrets) > 0 {
			d.Logger.Info("secrets resolved",
				"stack", stackName,
				"env_keys", slices.Sorted(maps.Keys(envVars)),
				"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
			)
		}

		deployRoot, err := os.OpenRoot(deployDir)
		if err != nil {
			return "", fmt.Errorf("opening deploy root: %w", err)
		}
		defer deployRoot.Close()

		iacRepoDir := filepath.Join(d.DataDir, "repo")
		merged, err := BuildEnvMap(stack.ConfigFile, iacRepoDir, envVars, d.Logger)
		if err != nil {
			return "", fmt.Errorf("building env map: %w", err)
		}
		if err := compose.WriteEnvFile(deployRoot, merged); err != nil {
			return "", fmt.Errorf("writing .env: %w", err)
		}

		if len(dockerSecrets) > 0 {
			secretsDir := filepath.Join(deployDir, "secrets")
			if err := os.MkdirAll(secretsDir, 0700); err != nil {
				return "", fmt.Errorf("creating secrets dir: %w", err)
			}
			secretsRoot, err := deployRoot.OpenRoot("secrets")
			if err != nil {
				return "", fmt.Errorf("opening secrets root: %w", err)
			}
			defer secretsRoot.Close()
			for name, val := range dockerSecrets {
				if err := compose.WriteSecret(secretsRoot, name, val); err != nil {
					return "", fmt.Errorf("writing docker secret %q: %w", name, err)
				}
			}
		}

		return fmt.Sprintf("%d env, %d docker", len(envVars), len(dockerSecrets)), nil
	})
	if deployErr != nil {
		return deployErr
	}

	// Resolve compose file path.
	var composeFile string
	if stack.Repo != "" {
		composeFile = resolveComposePath(stack.Compose, repoDir)
	} else {
		composeName, err := compose.FindComposeFile(repoDir)
		if err != nil {
			deployErr = fmt.Errorf("finding compose file: %w", err)
			return deployErr
		}
		composeFile = filepath.Join(repoDir, composeName)
	}

	defaultPort := "3000"
	if stack.Path != "" {
		defaultPort = "80"
	}

	// Generate compose override.
	deployErr = step("Compose override", func() error {
		deployRoot, err := os.OpenRoot(deployDir)
		if err != nil {
			return fmt.Errorf("opening deploy root: %w", err)
		}
		defer deployRoot.Close()

		envFilePaths := []string{filepath.Join(deployDir, ".env")}
		if stack.EnvFile != "" {
			envFilePaths = append(envFilePaths, stack.EnvFile)
		}
		data, err := GenerateOverride(OverrideParams{
			DeployDir:      deployDir,
			StackName:      stackName,
			Domain:         stack.Domain,
			ComposeFile:    composeFile,
			EnvFilePaths:   envFilePaths,
			DockerSecrets:  dockerSecrets,
			DefaultPort:    defaultPort,
			InternalNet:    "herald-" + stackName + "-internal",
			InlineOverride: stack.Override,
		})
		if err != nil {
			return err
		}
		f, err := deployRoot.OpenFile("compose.override.yml", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(data)
		return err
	})
	if deployErr != nil {
		return deployErr
	}

	// Ensure the caddy network exists before compose up.
	if err := caddy.EnsureNetwork(ctx, d.Logger); err != nil {
		deployErr = fmt.Errorf("ensuring caddy network: %w", err)
		return deployErr
	}

	// Compose up.
	deployErr = step("Compose up", func() error {
		return d.runCompose(ctx, deployDir, stackName, composeFile)
	})
	if deployErr != nil {
		return deployErr
	}

	// Post-deploy hook.
	if stack.UpdateScript != "" {
		deployErr = step("Post-deploy hook", func() error {
			return d.runPostDeployHook(ctx, stackName, stack, deployDir, composeFile)
		})
		if deployErr != nil {
			return deployErr
		}
	}

	// Write deployed ref for repo stacks only.
	if stack.Repo != "" {
		deployRef := effectiveRef(stack, ref)
		if commit, err := readDeployedCommit(repoDir); err == nil {
			_ = os.WriteFile(filepath.Join(deployDir, "deployed_ref"), []byte(deployRef+"@"+commit), 0644)
		}
	}

	d.Logger.Info("deploy complete", "stack", stackName, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// readDeployedCommit returns the short HEAD commit hash of the given repo dir.
func readDeployedCommit(repoDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := git.CmdWithAuth(ctx, "", repoDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitSync clones the repo on first deploy or fetch+reset on subsequent ones.
func (d *Deployer) gitSync(ctx context.Context, deployDir string, stack config.Stack, ref string) error {
	repoDir := filepath.Join(deployDir, "repo")
	d.Logger.Info("git sync", "repo", stack.Repo, "ref", ref)
	return git.CloneOrFetch(ctx, d.Config.Server.GithubToken, repoDir, git.RepoURL(stack.Repo), ref)
}

// symlinkSource creates <deployDir>/repo as a symlink pointing to <iacRepoDir>/<stack.Path>.
// If the symlink already exists and points to the right place, it is a no-op.
// If it points elsewhere (stale), it is removed and recreated.
func (d *Deployer) symlinkSource(deployDir string, stack config.Stack) error {
	iacRepoDir := filepath.Join(d.DataDir, "repo")
	target := filepath.Join(iacRepoDir, stack.Path)

	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("path %q not found in IaC repo", stack.Path)
	}

	repoLink := filepath.Join(deployDir, "repo")

	// If symlink already points to the right place, skip.
	if existing, err := os.Readlink(repoLink); err == nil {
		if existing == target {
			return nil
		}
		// Stale symlink: remove and recreate.
		if err := os.Remove(repoLink); err != nil {
			return fmt.Errorf("removing stale repo symlink: %w", err)
		}
	}

	if err := os.Symlink(target, repoLink); err != nil {
		return fmt.Errorf("creating repo symlink: %w", err)
	}
	d.Logger.Info("symlinked IaC path", "path", stack.Path, "target", target)
	return nil
}

// runPostDeployHook runs stack.UpdateScript as a post-deploy hook after compose up.
// The script path is resolved relative to the IaC repo root.
func (d *Deployer) runPostDeployHook(ctx context.Context, stackName string, stack config.Stack, deployDir, composeFile string) error {
	scriptPath := filepath.Join(d.DataDir, "repo", stack.UpdateScript)
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("update script %q not found in IaC repo", stack.UpdateScript)
	}

	env := append(os.Environ(),
		"STACK_NAME="+stackName,
		"STACK_DIR="+deployDir,
		"STACK_DOMAIN="+stack.Domain,
		"COMPOSE_FILE="+composeFile,
		"COMPOSE_OVERRIDE_FILE="+filepath.Join(deployDir, "compose.override.yml"),
	)

	d.Logger.Info("running post-deploy hook", "stack", stackName, "script", scriptPath)

	cmd := exec.CommandContext(ctx, "/bin/bash", "-euo", "pipefail", scriptPath)
	cmd.Dir = deployDir
	cmd.Env = env

	sw := d.ui().StreamWriter()
	if sw != nil {
		cmd.Stdout = sw
		cmd.Stderr = sw
		err := cmd.Run()
		ui.FlushStreamWriter(d.ui())
		return err
	}
	return runner.RunExecCmd(ctx, d.Logger, cmd)
}

// resolveComposePath returns an absolute path to the compose file.
// If the compose field is already absolute, use it directly.
// Otherwise, resolve relative to the repo directory.
func resolveComposePath(composeField, repoDir string) string {
	if filepath.IsAbs(composeField) {
		return composeField
	}
	return filepath.Join(repoDir, composeField)
}

// Down stops and removes the containers for the named stack.
// If removeVolumes is true, named volumes are also removed.
func (d *Deployer) Down(ctx context.Context, stackName string, removeVolumes bool) error {
	u := d.ui()
	start := time.Now()
	var downErr error
	defer func() {
		u.Done(stackName, downErr, time.Since(start))
	}()

	cctx, err := compose.ResolveStack(d.Config, stackName)
	if err != nil {
		downErr = err
		return downErr
	}

	args := cctx.BaseArgs()
	args = append(args, "--progress", "plain", "down", "--remove-orphans")
	if removeVolumes {
		args = append(args, "--volumes")
	}

	u.Step("Compose down")
	d.Logger.Info("compose down", "stack", stackName, "remove_volumes", removeVolumes)
	sw := u.StreamWriter()
	downErr = runner.RunCmdStream(ctx, d.Logger, cctx.WorkDir, sw, sw, "docker", args...)
	if downErr != nil {
		ui.FlushStreamWriter(u)
		u.StepFail(downErr)
		return downErr
	}
	ui.FlushStreamWriter(u)
	u.StepDone("")
	return nil
}

// runCompose executes docker compose up -d --build --remove-orphans.
func (d *Deployer) runCompose(ctx context.Context, deployDir, stackName, composeFile string) error {
	cctx := compose.Context{
		ProjectName:  "herald-" + stackName,
		ComposeFile:  composeFile,
		OverrideFile: filepath.Join(deployDir, "compose.override.yml"),
		EnvFile:      filepath.Join(deployDir, ".env"),
		WorkDir:      filepath.Join(deployDir, "repo"),
	}

	d.Logger.Info("compose up", "stack", stackName, "project", cctx.ProjectName)
	args := cctx.BaseArgs()
	args = append(args, "--progress", "plain", "up", "-d", "--build", "--remove-orphans")
	sw := d.ui().StreamWriter()
	err := runner.RunCmdStream(ctx, d.Logger, cctx.WorkDir, sw, sw, "docker", args...)
	ui.FlushStreamWriter(d.ui())
	return err
}
