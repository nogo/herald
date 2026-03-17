package stacks

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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/secrets"
)

// StackManager manages stack setup and updates.
type StackManager struct {
	Config  *config.Config
	Secrets *secrets.Store
	DataDir string // where the IaC repo lives (e.g. /etc/herald)
	Logger  *slog.Logger
}

// StackInfo holds display info about a configured stack.
type StackInfo struct {
	Name         string
	Domain       string
	AutoDeploy   bool
	UpdateScript string
}

// repoDir returns the path to the IaC repo clone.
func (m *StackManager) repoDir() string {
	return filepath.Join(m.DataDir, "repo")
}

// stackRepoPath returns the absolute path to the stack in the IaC repo.
func (m *StackManager) stackRepoPath(stack config.Stack) string {
	return filepath.Join(m.repoDir(), stack.Path)
}

// stackDeployDir returns the deploy directory for a named stack.
// e.g. /opt/deploy/stacks/nextcloud
func (m *StackManager) stackDeployDir(stackName string) string {
	return filepath.Join(m.Config.Server.StacksDir, "stacks", stackName)
}

// isSetUp returns true if the stack's deploy directory is already configured
// (repo symlink and compose override both exist).
func (m *StackManager) isSetUp(stackName string) bool {
	deployDir := m.stackDeployDir(stackName)
	_, repoErr := os.Lstat(filepath.Join(deployDir, "repo"))
	_, overrideErr := os.Lstat(filepath.Join(deployDir, "compose.override.yml"))
	return repoErr == nil && overrideErr == nil
}

// Setup creates the stack's deploy directory, symlinks the IaC repo stack
// directory, ensures the caddy network exists, and generates a compose override.
func (m *StackManager) Setup(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Stacks[stackName]
	if !ok {
		return fmt.Errorf("stack %q not found in config", stackName)
	}

	stackRepoPath := m.stackRepoPath(stack)
	if _, err := os.Stat(stackRepoPath); err != nil {
		return fmt.Errorf("stack path %q not found in IaC repo: %w", stack.Path, err)
	}

	deployDir := m.stackDeployDir(stackName)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return fmt.Errorf("creating deploy dir: %w", err)
	}

	// Symlink: <deploy_dir>/repo → <stackRepoPath>
	repoLink := filepath.Join(deployDir, "repo")
	if _, err := os.Lstat(repoLink); os.IsNotExist(err) {
		if err := os.Symlink(stackRepoPath, repoLink); err != nil {
			return fmt.Errorf("creating repo symlink: %w", err)
		}
		m.Logger.Info("created repo symlink", "stack", stackName, "target", stackRepoPath)
	}

	if err := caddy.EnsureNetwork(ctx, m.Logger); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}

	composeName, err := findComposeFile(repoLink)
	if err != nil {
		return err
	}
	composeFile := filepath.Join(repoLink, composeName)

	envVars, dockerSecrets, err := m.Secrets.Resolve(stack.Secrets)
	if err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}
	if len(envVars)+len(dockerSecrets) > 0 {
		m.Logger.Info("secrets resolved",
			"stack", stackName,
			"env_keys", slices.Sorted(maps.Keys(envVars)),
			"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
		)
	}

	if stack.EnvFile != "" {
		envMap, err := m.buildEnvMap(stack, envVars)
		if err != nil {
			return fmt.Errorf("building env map: %w", err)
		}
		if err := writeEnvFile(filepath.Join(deployDir, stack.EnvFile), envMap); err != nil {
			return fmt.Errorf("writing env file: %w", err)
		}
	}

	if len(dockerSecrets) > 0 {
		secretsDir := filepath.Join(deployDir, "secrets")
		if err := os.MkdirAll(secretsDir, 0700); err != nil {
			return fmt.Errorf("creating secrets dir: %w", err)
		}
		secretsRoot, err := os.OpenRoot(secretsDir)
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

	if err := m.generateOverride(deployDir, stackName, stack, composeFile, stack.EnvFile, dockerSecrets); err != nil {
		return fmt.Errorf("generating compose override: %w", err)
	}

	m.Logger.Info("stack setup complete", "stack", stackName, "deploy_dir", deployDir)
	return nil
}

// Update pulls the IaC repo, sets up the stack if needed, and runs the update script.
func (m *StackManager) Update(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Stacks[stackName]
	if !ok {
		return fmt.Errorf("stack %q not found in config", stackName)
	}

	start := time.Now()
	m.Logger.Info("stack update started", "stack", stackName)

	if err := m.gitPull(ctx); err != nil {
		return fmt.Errorf("pulling IaC repo: %w", err)
	}

	if stack.UpdateScript == "" {
		return fmt.Errorf("stack %q has no update_script configured", stackName)
	}
	scriptPath := filepath.Join(m.repoDir(), stack.UpdateScript)
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("update script %q not found in server repo", stack.UpdateScript)
	}

	if !m.isSetUp(stackName) {
		if err := m.Setup(ctx, stackName); err != nil {
			return fmt.Errorf("setting up stack: %w", err)
		}
	}

	deployDir := m.stackDeployDir(stackName)
	repoLink := filepath.Join(deployDir, "repo")

	envVars, dockerSecrets, err := m.Secrets.Resolve(stack.Secrets)
	if err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}
	if len(envVars)+len(dockerSecrets) > 0 {
		m.Logger.Info("secrets resolved",
			"stack", stackName,
			"env_keys", slices.Sorted(maps.Keys(envVars)),
			"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
		)
	}

	if stack.EnvFile != "" {
		envMap, err := m.buildEnvMap(stack, envVars)
		if err != nil {
			return fmt.Errorf("building env map: %w", err)
		}
		if err := writeEnvFile(filepath.Join(deployDir, stack.EnvFile), envMap); err != nil {
			return fmt.Errorf("writing env file: %w", err)
		}
	}

	if len(dockerSecrets) > 0 {
		secretsDir := filepath.Join(deployDir, "secrets")
		if err := os.MkdirAll(secretsDir, 0700); err != nil {
			return fmt.Errorf("creating secrets dir: %w", err)
		}
		secretsRoot, err := os.OpenRoot(secretsDir)
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

	composeName, err := findComposeFile(repoLink)
	if err != nil {
		return err
	}
	composeFile := filepath.Join(repoLink, composeName)
	if err := m.generateOverride(deployDir, stackName, stack, composeFile, stack.EnvFile, dockerSecrets); err != nil {
		return fmt.Errorf("generating compose override: %w", err)
	}

	if err := m.RunUpdateScript(ctx, stackName); err != nil {
		return err
	}

	m.Logger.Info("stack update complete", "stack", stackName, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// ComposeUp runs docker compose up -d --build for the stack without running the update script.
func (m *StackManager) ComposeUp(ctx context.Context, stackName string) error {
	if _, ok := m.Config.Stacks[stackName]; !ok {
		return fmt.Errorf("stack %q not found in config", stackName)
	}

	if !m.isSetUp(stackName) {
		if err := m.Setup(ctx, stackName); err != nil {
			return fmt.Errorf("setting up stack: %w", err)
		}
	}

	deployDir := m.stackDeployDir(stackName)
	repoLink := filepath.Join(deployDir, "repo")

	composeName, err := findComposeFile(repoLink)
	if err != nil {
		return err
	}
	composeFile := filepath.Join(repoLink, composeName)
	overrideFile := filepath.Join(deployDir, "compose.override.yml")

	m.Logger.Info("compose up", "stack", stackName, "project", "herald-stack-"+stackName)
	return runStreamCmd(ctx, m.Logger, deployDir,
		"docker", "compose",
		"--project-name", "herald-stack-"+stackName,
		"-f", composeFile,
		"-f", overrideFile,
		"up", "-d", "--build", "--remove-orphans",
	)
}

// RunUpdateScript executes the stack's update script with the required environment.
func (m *StackManager) RunUpdateScript(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Stacks[stackName]
	if !ok {
		return fmt.Errorf("stack %q not found in config", stackName)
	}
	if stack.UpdateScript == "" {
		return fmt.Errorf("stack %q has no update_script configured", stackName)
	}

	scriptPath := filepath.Join(m.repoDir(), stack.UpdateScript)
	deployDir := m.stackDeployDir(stackName)
	repoLink := filepath.Join(deployDir, "repo")

	composeName, _ := findComposeFile(repoLink)
	if composeName == "" {
		composeName = "compose.yaml"
	}
	composeFile := filepath.Join(repoLink, composeName)

	env := append(os.Environ(),
		"STACK_NAME="+stackName,
		"STACK_DIR="+deployDir,
		"STACK_DOMAIN="+stack.Domain,
		"COMPOSE_FILE="+composeFile,
		"COMPOSE_OVERRIDE_FILE="+filepath.Join(deployDir, "compose.override.yml"),
	)

	m.Logger.Info("running update script", "stack", stackName, "script", scriptPath, "dir", deployDir)

	cmd := exec.CommandContext(ctx, "/bin/bash", "-euo", "pipefail", scriptPath)
	cmd.Dir = deployDir
	cmd.Env = env

	ring := newRingBuffer(50)
	outWriter := &lineWriter{logger: m.Logger, cmd: "update-script[" + stackName + "]", level: slog.LevelInfo, ring: ring}
	errWriter := &lineWriter{logger: m.Logger, cmd: "update-script[" + stackName + "]", level: slog.LevelWarn, ring: ring}
	cmd.Stdout = outWriter
	cmd.Stderr = errWriter

	start := time.Now()
	runErr := cmd.Run()
	outWriter.flush()
	errWriter.flush()
	dur := time.Since(start).Round(time.Millisecond)

	if runErr != nil {
		exitCode := -1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		lastLines := strings.Join(ring.get(), "\n")
		return fmt.Errorf("update script exited with code %d (duration: %s):\n%s", exitCode, dur, lastLines)
	}

	m.Logger.Info("update script completed", "stack", stackName, "duration", dur)
	return nil
}

// List returns info about all configured stacks, sorted by name.
func (m *StackManager) List() []StackInfo {
	names := slices.Sorted(maps.Keys(m.Config.Stacks))
	result := make([]StackInfo, 0, len(names))
	for _, name := range names {
		s := m.Config.Stacks[name]
		result = append(result, StackInfo{
			Name:         name,
			Domain:       s.Domain,
			AutoDeploy:   s.AutoDeploy,
			UpdateScript: s.UpdateScript,
		})
	}
	return result
}

// gitPull runs git pull in the IaC repo with auth for private repos.
func (m *StackManager) gitPull(ctx context.Context) error {
	repoDir := m.repoDir()
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return fmt.Errorf("IaC repo not found at %s (run: herald init)", repoDir)
	}
	m.Logger.Info("pulling IaC repo", "dir", repoDir)
	output, err := githelper.PullFFOnly(ctx, m.Config.Server.GithubToken, repoDir)
	if err != nil {
		return fmt.Errorf("git pull: %s", output)
	}
	m.Logger.Info("git pull", "output", output)
	return nil
}

// findComposeFile returns the first compose filename found in dir.
func findComposeFile(dir string) (string, error) {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", dir)
}

// validateConfigFilePath returns an error if p is absolute or contains ".." components.
func validateConfigFilePath(p string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("config file path must be relative, got %q", p)
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return fmt.Errorf("config file path must not contain '..': %q", p)
		}
	}
	return nil
}

// loadConfigFile parses a KEY=VALUE env file, skipping comments and blank lines.
func (m *StackManager) loadConfigFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("config file %q not found in IaC repo", path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			m.Logger.Debug("config file: ignoring line without '='", "line", trimmed)
			continue
		}
		result[strings.TrimSpace(trimmed[:idx])] = strings.TrimSpace(trimmed[idx+1:])
	}
	return result, nil
}

// buildEnvMap returns the merged env map: config file base overlaid with resolved secrets.
func (m *StackManager) buildEnvMap(stack config.Stack, envVars map[string]string) (map[string]string, error) {
	if stack.ConfigFile == "" {
		return envVars, nil
	}
	if err := validateConfigFilePath(stack.ConfigFile); err != nil {
		return nil, err
	}
	base, err := m.loadConfigFile(filepath.Join(m.repoDir(), stack.ConfigFile))
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(base)+len(envVars))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range envVars {
		if _, exists := result[k]; exists {
			m.Logger.Debug("config key overridden by secret", "key", k)
		}
		result[k] = v
	}
	return result, nil
}

// writeEnvFile writes sorted KEY=VALUE pairs to the given file path.
func writeEnvFile(path string, envMap map[string]string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(envMap)) {
		if _, werr := fmt.Fprintf(f, "%s=%s\n", key, envMap[key]); werr != nil {
			f.Close()
			return werr
		}
	}
	return f.Close()
}

func (m *StackManager) generateOverride(deployDir, stackName string, stack config.Stack, composeFile string, envFile string, dockerSecrets map[string]string) error {
	serviceName, port, err := compose.DetectServiceInfo(composeFile, stackName, "80")
	if err != nil {
		m.Logger.Warn("could not detect service info, using defaults", "stack", stackName, "error", err)
		serviceName = "app"
		port = "80"
	}

	svc := compose.ServiceOverride{
		Labels: map[string]string{
			"caddy":               stack.Domain,
			"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %s}}", port),
		},
		Networks: []string{"caddy"},
	}

	if envFile != "" {
		svc.EnvFile = []string{filepath.Join(deployDir, envFile)}
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
				File: filepath.Join(deployDir, "secrets", name),
			}
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return fmt.Errorf("marshaling compose override: %w", err)
	}

	return os.WriteFile(filepath.Join(deployDir, "compose.override.yml"), data, 0644)
}

// --- Streaming command runner ---

// runStreamCmd executes a command, streaming stdout/stderr to the logger in real-time.
func runStreamCmd(ctx context.Context, logger *slog.Logger, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	outWriter := &lineWriter{logger: logger, cmd: name, level: slog.LevelInfo}
	errWriter := &lineWriter{logger: logger, cmd: name, level: slog.LevelWarn}
	cmd.Stdout = outWriter
	cmd.Stderr = errWriter

	start := time.Now()
	runErr := cmd.Run()
	outWriter.flush()
	errWriter.flush()
	dur := time.Since(start).Round(time.Millisecond)

	if runErr != nil {
		return fmt.Errorf("%s: %w (duration: %s)", name, runErr, dur)
	}
	logger.Info("command completed", "cmd", name, "duration", dur)
	return nil
}

// lineWriter buffers by newline and logs each complete line in real-time.
type lineWriter struct {
	logger *slog.Logger
	cmd    string
	level  slog.Level
	buf    []byte
	ring   *ringBuffer // optional; if set, also records lines for error reporting
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r")
		w.buf = w.buf[idx+1:]
		if line == "" {
			continue
		}
		w.logger.Log(context.Background(), w.level, line, "cmd", w.cmd)
		if w.ring != nil {
			w.ring.add(line)
		}
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) == 0 {
		return
	}
	line := strings.TrimRight(string(w.buf), "\r\n")
	w.buf = nil
	if line == "" {
		return
	}
	w.logger.Log(context.Background(), w.level, line, "cmd", w.cmd)
	if w.ring != nil {
		w.ring.add(line)
	}
}

// ringBuffer keeps the last N lines for error reporting.
type ringBuffer struct {
	lines []string
	max   int
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (r *ringBuffer) add(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ringBuffer) get() []string {
	return r.lines
}
