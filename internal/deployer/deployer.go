package deployer

import (
	"bytes"
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

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/config"
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
	if err := writeEnvFile(appRoot, envVars); err != nil {
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
			if err := writeSecret(secretsRoot, name, val); err != nil {
				return fmt.Errorf("writing docker secret %q: %w", name, err)
			}
		}
	}

	// 6. Generate compose override.
	repoDir := filepath.Join(appDir, "repo")
	composeFile := app.Compose
	if err := d.generateOverride(appRoot, appDir, appName, app, repoDir, composeFile, dockerSecrets); err != nil {
		return fmt.Errorf("generating compose override: %w", err)
	}

	// 7. Ensure the caddy network exists before compose up.
	if err := ensureCaddyNetwork(ctx, d.Logger); err != nil {
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
	url := repoURL(app.Repo)
	token := d.Config.Server.GithubToken

	_, err := os.Stat(repoDir)
	if os.IsNotExist(err) {
		d.Logger.Info("git clone", "repo", app.Repo, "branch", app.Branch)
		cmd := gitCmdWithAuth(ctx, token, "",
			"clone", "--branch", app.Branch,
			"--single-branch", "--depth", "1",
			url, repoDir,
		)
		return runExecCmd(ctx, d.Logger, cmd)
	}
	if err != nil {
		return fmt.Errorf("stat repo dir: %w", err)
	}

	d.Logger.Info("git fetch+reset", "repo", app.Repo, "branch", app.Branch)
	fetchCmd := gitCmdWithAuth(ctx, token, repoDir, "fetch", "origin", app.Branch)
	if err := runExecCmd(ctx, d.Logger, fetchCmd); err != nil {
		return err
	}
	resetCmd := gitCmdWithAuth(ctx, token, repoDir, "reset", "--hard", "origin/"+app.Branch)
	return runExecCmd(ctx, d.Logger, resetCmd)
}

// repoURL returns the HTTPS clone URL without credentials.
func repoURL(repo string) string {
	return fmt.Sprintf("https://github.com/%s.git", repo)
}

// gitCmdWithAuth creates an exec.Cmd for git with token-based auth via
// environment variables. Avoids embedding tokens in URLs (visible in ps/proc
// and .git/config). Uses git's credential helper protocol via GIT_CONFIG_*
// environment variables.
func gitCmdWithAuth(ctx context.Context, token, dir string, args ...string) *exec.Cmd {
	// Disable git hooks for security (prevent code execution from cloned repos).
	gitArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		// Use a credential helper that returns the token.
		// This keeps the token out of the command line and .git/config.
		helper := fmt.Sprintf("!f() { echo username=x-access-token; echo password=%s; }; f", token)
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0="+helper,
		)
	}
	return cmd
}

// writeEnvFile writes KEY=value pairs to .env, sorted by key.
func writeEnvFile(root *os.Root, envVars map[string]string) error {
	f, err := root.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, key := range slices.Sorted(maps.Keys(envVars)) {
		if _, err := fmt.Fprintf(f, "%s=%s\n", key, envVars[key]); err != nil {
			return err
		}
	}
	return nil
}

// writeSecret writes a docker secret value to a file in the secrets root.
func writeSecret(root *os.Root, name, value string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(value)
	return err
}

// --- Compose override generation ---

type composeOverride struct {
	Services map[string]serviceOverride `yaml:"services"`
	Networks map[string]networkDef      `yaml:"networks,omitempty"`
	Secrets  map[string]secretFileDef   `yaml:"secrets,omitempty"`
}

type serviceOverride struct {
	Labels   map[string]string `yaml:"labels,omitempty"`
	EnvFile  []string          `yaml:"env_file,omitempty"`
	Secrets  []string          `yaml:"secrets,omitempty"`
	Networks []string          `yaml:"networks,omitempty"`
}

type networkDef struct {
	External bool `yaml:"external"`
}

type secretFileDef struct {
	File string `yaml:"file"`
}

type minimalCompose struct {
	Services map[string]minimalService `yaml:"services"`
}

type minimalService struct {
	Expose []any `yaml:"expose"`
	Ports  []any `yaml:"ports"`
}

func (d *Deployer) generateOverride(
	root *os.Root,
	appDir, appName string,
	app config.App,
	repoDir, composeFile string,
	dockerSecrets map[string]string,
) error {
	// Detect service name and port from the app's compose file.
	serviceName, port, err := detectServiceInfo(filepath.Join(repoDir, composeFile), appName)
	if err != nil {
		d.Logger.Warn("could not detect service info, using defaults",
			"app", appName, "error", err)
		serviceName = "app"
		port = "3000"
	}

	svc := serviceOverride{
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

	override := composeOverride{
		Services: map[string]serviceOverride{serviceName: svc},
		Networks: map[string]networkDef{"caddy": {External: true}},
	}

	if len(secretNames) > 0 {
		override.Secrets = make(map[string]secretFileDef, len(secretNames))
		for _, name := range secretNames {
			override.Secrets[name] = secretFileDef{
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
		merged := deepMerge(baseMap, overlayMap)
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

// detectServiceInfo parses a compose file to find the main service name and port.
// Prefers a service named "app", then appName, then the first alphabetically.
func detectServiceInfo(composeFilePath, appName string) (serviceName, port string, err error) {
	data, err := os.ReadFile(composeFilePath)
	if err != nil {
		return "", "", err
	}

	var mc minimalCompose
	if err := yaml.Unmarshal(data, &mc); err != nil {
		return "", "", err
	}

	if len(mc.Services) == 0 {
		return "app", "3000", nil
	}

	names := slices.Sorted(maps.Keys(mc.Services))
	serviceName = names[0]
	for _, n := range names {
		if n == "app" || n == appName {
			serviceName = n
			break
		}
	}

	port = extractFirstPort(mc.Services[serviceName])
	if port == "" {
		port = "3000"
	}
	return serviceName, port, nil
}

func extractFirstPort(svc minimalService) string {
	for _, v := range svc.Expose {
		if p := portFromAny(v); p != "" {
			return p
		}
	}
	for _, v := range svc.Ports {
		if p := portFromAny(v); p != "" {
			return p
		}
	}
	return ""
}

func portFromAny(v any) string {
	switch val := v.(type) {
	case string:
		// "3000", "3000:3000", "0.0.0.0:80:3000"
		parts := strings.Split(val, ":")
		p := strings.TrimSpace(parts[len(parts)-1])
		return p
	case int:
		return fmt.Sprintf("%d", val)
	case map[string]any:
		// long form: {target: 3000, published: 3000}
		if t, ok := val["target"]; ok {
			return portFromAny(t)
		}
	}
	return ""
}

// deepMerge recursively merges overlay into base. Overlay wins on conflict
// unless both values are maps, in which case they are merged recursively.
func deepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if bv, ok := result[k]; ok {
			if bMap, ok := bv.(map[string]any); ok {
				if oMap, ok := v.(map[string]any); ok {
					result[k] = deepMerge(bMap, oMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}

// ensureCaddyNetwork creates the caddy Docker network if it doesn't exist.
func ensureCaddyNetwork(ctx context.Context, logger *slog.Logger) error {
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", "caddy")
	if err := cmd.Run(); err == nil {
		return nil
	}
	return runCmd(ctx, logger, "", "docker", "network", "create", "caddy")
}

// runCompose executes docker compose up -d --build --remove-orphans.
func (d *Deployer) runCompose(ctx context.Context, appDir, appName, composeFile string) error {
	repoDir := filepath.Join(appDir, "repo")
	overrideFile := filepath.Join(appDir, "compose.override.yml")
	d.Logger.Info("compose up", "app", appName, "project", "herald-"+appName)
	return runCmd(ctx, d.Logger, repoDir,
		"docker", "compose",
		"--project-name", "herald-"+appName,
		"-f", composeFile,
		"-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans",
	)
}

// runCmd executes a command, logs stdout/stderr line-by-line, and returns an
// error on non-zero exit.
func runCmd(ctx context.Context, logger *slog.Logger, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return runExecCmd(ctx, logger, cmd)
}

// runExecCmd executes a pre-built command, logs stdout/stderr, returns error on
// non-zero exit. Use this when the command needs custom Env or other settings.
func runExecCmd(ctx context.Context, logger *slog.Logger, cmd *exec.Cmd) error {
	name := cmd.Path

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start).Round(time.Millisecond)

	logLines := func(output string, level slog.Level) {
		for line := range strings.Lines(output) {
			line = strings.TrimRight(line, "\n\r")
			if line != "" {
				logger.Log(ctx, level, line, "cmd", name)
			}
		}
	}

	logLines(stdout.String(), slog.LevelInfo)
	if stderr.Len() > 0 {
		logLines(stderr.String(), slog.LevelWarn)
	}

	if runErr != nil {
		return fmt.Errorf("%s: %w (duration: %s)", name, runErr, dur)
	}

	logger.Info("command completed", "cmd", name, "duration", dur)
	return nil
}
