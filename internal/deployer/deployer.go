package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/runner"
	"github.com/nogo/herald/internal/secrets"
)

// Deployer executes app deploys.
type Deployer struct {
	Config  *config.Config
	Secrets *secrets.Store
	Logger  *slog.Logger

	appLocks sync.Map // string → *appLock
	wg       sync.WaitGroup
}

type appLock struct {
	mu    sync.Mutex
	count atomic.Int32 // goroutines running or waiting, max 2
}

func (d *Deployer) getAppLock(appName string) *appLock {
	v, _ := d.appLocks.LoadOrStore(appName, &appLock{})
	return v.(*appLock)
}

// DeployAsync dispatches a deploy in a goroutine with per-app serialization.
// At most one deploy may be queued per app; additional calls are dropped.
func (d *Deployer) DeployAsync(appName string) {
	lock := d.getAppLock(appName)
	if lock.count.Add(1) > 2 {
		lock.count.Add(-1)
		d.Logger.Info("deploy already queued, dropping", "app", appName)
		return
	}

	d.wg.Go(func() {
		lock.mu.Lock()
		defer lock.mu.Unlock()
		defer lock.count.Add(-1)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := d.Deploy(ctx, appName); err != nil {
			d.Logger.Error("deploy failed", "app", appName, "error", err)
		}
	})
}

// Wait blocks until all in-progress deploys finish.
func (d *Deployer) Wait() {
	d.wg.Wait()
}

// Deploy executes a full deploy for the named app.
func (d *Deployer) Deploy(ctx context.Context, appName string) error {
	app, ok := d.Config.Apps[appName]
	if !ok {
		return fmt.Errorf("app %q not found in config", appName)
	}

	// Pre-flight: check for missing required secrets.
	missing, err := d.Secrets.MissingRequired(app.Secrets)
	if err != nil {
		return fmt.Errorf("checking secrets: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("app %q: missing required secrets (use `herald secret set <key>`): %s",
			appName, strings.Join(missing, ", "))
	}

	appDir := filepath.Join(d.Config.Server.StacksDir, "apps", appName)
	start := time.Now()
	d.Logger.Info("deploy started", "app", appName, "dir", appDir)

	if err := os.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("creating app dir: %w", err)
	}

	// 1. Git clone or pull.
	if err := d.gitSync(ctx, appDir, app); err != nil {
		return fmt.Errorf("git: %w", err)
	}

	// 2. Verify env_file exists if configured.
	if app.EnvFile != "" {
		if _, err := os.Stat(app.EnvFile); err != nil {
			return fmt.Errorf("env_file %q not found", app.EnvFile)
		}
	}

	// 3. Resolve secrets.
	envVars, dockerSecrets, err := d.Secrets.Resolve(app.Secrets)
	if err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}
	if len(envVars)+len(dockerSecrets) > 0 {
		d.Logger.Info("secrets resolved",
			"app", appName,
			"env_keys", slices.Sorted(maps.Keys(envVars)),
			"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
		)
	}

	// Open root for scoped file writes.
	appRoot, err := os.OpenRoot(appDir)
	if err != nil {
		return fmt.Errorf("opening app root: %w", err)
	}
	defer appRoot.Close()

	// 4. Write .env file.
	if err := compose.WriteEnvFile(appRoot, envVars); err != nil {
		return fmt.Errorf("writing .env: %w", err)
	}

	// 5. Write docker secrets.
	if len(dockerSecrets) > 0 {
		secretsDir := filepath.Join(appDir, "secrets")
		if err := os.MkdirAll(secretsDir, 0700); err != nil {
			return fmt.Errorf("creating secrets dir: %w", err)
		}
		secretsRoot, err := appRoot.OpenRoot("secrets")
		if err != nil {
			return fmt.Errorf("opening secrets root: %w", err)
		}
		defer secretsRoot.Close()
		for name, val := range dockerSecrets {
			if err := compose.WriteSecret(secretsRoot, name, val); err != nil {
				return fmt.Errorf("writing docker secret %q: %w", name, err)
			}
		}
	}

	// 6. Generate compose override.
	repoDir := filepath.Join(appDir, "repo")
	composeFile := resolveComposePath(app.Compose, repoDir)
	if err := d.generateOverride(appRoot, appDir, appName, app, composeFile, dockerSecrets); err != nil {
		return fmt.Errorf("generating compose override: %w", err)
	}

	// 7. Ensure the caddy network exists before compose up.
	if err := caddy.EnsureNetwork(ctx, d.Logger); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}

	// 8. Run docker compose up.
	if err := d.runCompose(ctx, appDir, appName, composeFile); err != nil {
		return fmt.Errorf("compose: %w", err)
	}

	d.Logger.Info("deploy complete", "app", appName, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// gitSync clones the repo on first deploy or fetch+reset on subsequent ones.
func (d *Deployer) gitSync(ctx context.Context, appDir string, app config.App) error {
	repoDir := filepath.Join(appDir, "repo")
	d.Logger.Info("git sync", "repo", app.Repo, "branch", app.Branch)
	return git.CloneOrFetch(ctx, d.Config.Server.GithubToken, repoDir, git.RepoURL(app.Repo), app.Branch)
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

func (d *Deployer) generateOverride(
	root *os.Root,
	appDir, appName string,
	app config.App,
	composeFile string,
	dockerSecrets map[string]string,
) error {
	// Detect service name and port from the app's compose file.
	serviceName, port, err := compose.DetectServiceInfo(composeFile, appName, "3000")
	if err != nil {
		d.Logger.Warn("could not detect service info, using defaults",
			"app", appName, "error", err)
		serviceName = "app"
		port = "3000"
	}

	svc := compose.ServiceOverride{
		Labels: map[string]string{
			"caddy":               app.Domain,
			"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %s}}", port),
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
				File: filepath.Join(appDir, "secrets", name),
			}
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return fmt.Errorf("marshaling override: %w", err)
	}

	// If app has inline override YAML, deep-merge it.
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

// runCompose executes docker compose up -d --build --remove-orphans.
func (d *Deployer) runCompose(ctx context.Context, appDir, appName, composeFile string) error {
	repoDir := filepath.Join(appDir, "repo")
	overrideFile := filepath.Join(appDir, "compose.override.yml")
	d.Logger.Info("compose up", "app", appName, "project", "herald-"+appName)
	return runner.RunCmd(ctx, d.Logger, repoDir,
		"docker", "compose",
		"--project-name", "herald-"+appName,
		"-f", composeFile,
		"-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans",
	)
}
