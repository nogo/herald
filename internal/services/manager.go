package services

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

	"io"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/compose"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/ui"
)

// ServiceManager manages service setup and updates.
type ServiceManager struct {
	Config  *config.Config
	Secrets *secrets.Store
	DataDir string // where the IaC repo lives (e.g. /etc/herald)
	Logger  *slog.Logger
	UI      ui.UI // optional; nil defaults to ui.Nop()
}

func (m *ServiceManager) ui() ui.UI {
	if m.UI != nil {
		return m.UI
	}
	return ui.Nop()
}

// ServiceInfo holds display info about a configured service.
type ServiceInfo struct {
	Name         string
	Domain       string
	AutoDeploy   bool
	UpdateScript string
}

// repoDir returns the path to the IaC repo clone.
func (m *ServiceManager) repoDir() string {
	return filepath.Join(m.DataDir, "repo")
}

// stackRepoPath returns the absolute path to the service in the IaC repo.
func (m *ServiceManager) stackRepoPath(stack config.Service) string {
	return filepath.Join(m.repoDir(), stack.Path)
}

// stackDeployDir returns the deploy directory for a named service.
// e.g. /opt/deploy/services/nextcloud
func (m *ServiceManager) stackDeployDir(stackName string) string {
	return filepath.Join(m.Config.Server.ServicesDir, "services", stackName)
}

// isSetUp returns true if the service's deploy directory is already configured
// (repo symlink and compose override both exist).
func (m *ServiceManager) isSetUp(stackName string) bool {
	deployDir := m.stackDeployDir(stackName)
	_, repoErr := os.Lstat(filepath.Join(deployDir, "repo"))
	_, overrideErr := os.Lstat(filepath.Join(deployDir, "compose.override.yml"))
	return repoErr == nil && overrideErr == nil
}

// Setup creates the service's deploy directory, symlinks the IaC repo service
// directory, ensures the caddy network exists, and generates a compose override.
func (m *ServiceManager) Setup(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Services[stackName]
	if !ok {
		return fmt.Errorf("service %q not found in config", stackName)
	}

	// Pre-flight: check for missing required secrets.
	missing, err := m.Secrets.MissingRequired(stack.Secrets)
	if err != nil {
		return fmt.Errorf("checking secrets: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("service %q: missing required secrets (use `herald secret set <key>`): %s",
			stackName, strings.Join(missing, ", "))
	}

	stackRepoPath := m.stackRepoPath(stack)
	if _, err := os.Stat(stackRepoPath); err != nil {
		return fmt.Errorf("service path %q not found in IaC repo: %w", stack.Path, err)
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
		m.Logger.Info("created repo symlink", "service", stackName, "target", stackRepoPath)
	}

	if err := caddy.EnsureNetwork(ctx, m.Logger); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}

	composeName, err := compose.FindComposeFile(repoLink)
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
			"service", stackName,
			"env_keys", slices.Sorted(maps.Keys(envVars)),
			"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
		)
	}

	if stack.EnvFile != "" {
		envMap, err := deployer.BuildEnvMap(stack.ConfigFile, m.repoDir(), envVars, m.Logger)
		if err != nil {
			return fmt.Errorf("building env map: %w", err)
		}
		if err := deployer.WriteEnvFile(filepath.Join(deployDir, stack.EnvFile), envMap); err != nil {
			return fmt.Errorf("writing env file: %w", err)
		}
	}

	if len(dockerSecrets) > 0 {
		if err := deployer.WriteDockerSecrets(filepath.Join(deployDir, "secrets"), dockerSecrets); err != nil {
			return fmt.Errorf("writing docker secrets: %w", err)
		}
	}

	var envFilePaths []string
	if stack.EnvFile != "" {
		envFilePaths = []string{filepath.Join(deployDir, stack.EnvFile)}
	}
	data, err := deployer.GenerateOverride(deployer.OverrideParams{
		DeployDir:     deployDir,
		StackName:     stackName,
		Domain:        stack.Domain,
		ComposeFile:   composeFile,
		EnvFilePaths:  envFilePaths,
		DockerSecrets: dockerSecrets,
		DefaultPort:   "80",
		InternalNet:   "herald-svc-" + stackName + "-internal",
	})
	if err != nil {
		return fmt.Errorf("generating compose override: %w", err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "compose.override.yml"), data, 0644); err != nil {
		return fmt.Errorf("writing compose override: %w", err)
	}

	m.Logger.Info("service setup complete", "service", stackName, "deploy_dir", deployDir)
	return nil
}

// Update pulls the IaC repo, sets up the service if needed, and runs the update script.
func (m *ServiceManager) Update(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Services[stackName]
	if !ok {
		return fmt.Errorf("service %q not found in config", stackName)
	}

	u := m.ui()
	start := time.Now()
	var updateErr error
	defer func() {
		u.Done(stackName, updateErr, time.Since(start))
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

	// Pre-flight.
	updateErr = step("Preflight", func() error {
		missing, err := m.Secrets.MissingRequired(stack.Secrets)
		if err != nil {
			return fmt.Errorf("checking secrets: %w", err)
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing secrets (use `herald secret set <key>`): %s",
				strings.Join(missing, ", "))
		}
		return nil
	})
	if updateErr != nil {
		return updateErr
	}

	m.Logger.Info("service update started", "service", stackName)

	// Git pull.
	updateErr = step("Git pull", func() error {
		return m.gitPull(ctx)
	})
	if updateErr != nil {
		return updateErr
	}

	if stack.UpdateScript == "" {
		updateErr = fmt.Errorf("service %q has no update_script configured", stackName)
		return updateErr
	}
	scriptPath := filepath.Join(m.repoDir(), stack.UpdateScript)
	if _, err := os.Stat(scriptPath); err != nil {
		updateErr = fmt.Errorf("update script %q not found in server repo", stack.UpdateScript)
		return updateErr
	}

	// Setup if needed.
	if !m.isSetUp(stackName) {
		updateErr = step("Setup", func() error {
			return m.Setup(ctx, stackName)
		})
		if updateErr != nil {
			return updateErr
		}
	}

	// Secrets + env + override.
	updateErr = step("Secrets", func() error {
		deployDir := m.stackDeployDir(stackName)
		repoLink := filepath.Join(deployDir, "repo")

		envVars, dockerSecrets, err := m.Secrets.Resolve(stack.Secrets)
		if err != nil {
			return fmt.Errorf("resolving secrets: %w", err)
		}
		if len(envVars)+len(dockerSecrets) > 0 {
			m.Logger.Info("secrets resolved",
				"service", stackName,
				"env_keys", slices.Sorted(maps.Keys(envVars)),
				"docker_secret_keys", slices.Sorted(maps.Keys(dockerSecrets)),
			)
		}

		if stack.EnvFile != "" {
			envMap, err := deployer.BuildEnvMap(stack.ConfigFile, m.repoDir(), envVars, m.Logger)
			if err != nil {
				return fmt.Errorf("building env map: %w", err)
			}
			if err := deployer.WriteEnvFile(filepath.Join(deployDir, stack.EnvFile), envMap); err != nil {
				return fmt.Errorf("writing env file: %w", err)
			}
		}

		if len(dockerSecrets) > 0 {
			if err := deployer.WriteDockerSecrets(filepath.Join(deployDir, "secrets"), dockerSecrets); err != nil {
				return fmt.Errorf("writing docker secrets: %w", err)
			}
		}

		composeName, err := compose.FindComposeFile(repoLink)
		if err != nil {
			return err
		}
		composeFile := filepath.Join(repoLink, composeName)

		var envFilePaths []string
		if stack.EnvFile != "" {
			envFilePaths = []string{filepath.Join(deployDir, stack.EnvFile)}
		}
		data, err := deployer.GenerateOverride(deployer.OverrideParams{
			DeployDir:     deployDir,
			StackName:     stackName,
			Domain:        stack.Domain,
			ComposeFile:   composeFile,
			EnvFilePaths:  envFilePaths,
			DockerSecrets: dockerSecrets,
			DefaultPort:   "80",
			InternalNet:   "herald-svc-" + stackName + "-internal",
		})
		if err != nil {
			return fmt.Errorf("generating compose override: %w", err)
		}
		return os.WriteFile(filepath.Join(deployDir, "compose.override.yml"), data, 0644)
	})
	if updateErr != nil {
		return updateErr
	}

	// Run update script.
	updateErr = step("Update script", func() error {
		return m.RunUpdateScript(ctx, stackName)
	})
	if updateErr != nil {
		return updateErr
	}

	m.Logger.Info("service update complete", "service", stackName, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// ComposeUp runs docker compose up -d --build for the service without running the update script.
func (m *ServiceManager) ComposeUp(ctx context.Context, stackName string) error {
	if _, ok := m.Config.Services[stackName]; !ok {
		return fmt.Errorf("service %q not found in config", stackName)
	}

	if !m.isSetUp(stackName) {
		if err := m.Setup(ctx, stackName); err != nil {
			return fmt.Errorf("setting up service: %w", err)
		}
	}

	cctx, err := compose.ResolveService(m.Config, stackName)
	if err != nil {
		return err
	}

	m.Logger.Info("compose up", "service", stackName, "project", cctx.ProjectName)
	args := cctx.BaseArgs()
	args = append(args, "up", "-d", "--build", "--remove-orphans")
	return runStreamCmd(ctx, m.Logger, cctx.WorkDir, "docker", args...)
}

// RunUpdateScript executes the service's update script with the required environment.
func (m *ServiceManager) RunUpdateScript(ctx context.Context, stackName string) error {
	stack, ok := m.Config.Services[stackName]
	if !ok {
		return fmt.Errorf("service %q not found in config", stackName)
	}
	if stack.UpdateScript == "" {
		return fmt.Errorf("service %q has no update_script configured", stackName)
	}

	scriptPath := filepath.Join(m.repoDir(), stack.UpdateScript)
	deployDir := m.stackDeployDir(stackName)
	repoLink := filepath.Join(deployDir, "repo")

	composeName, _ := compose.FindComposeFile(repoLink)
	if composeName == "" {
		composeName = "compose.yaml"
	}
	composeFile := filepath.Join(repoLink, composeName)

	env := append(os.Environ(),
		"SERVICE_NAME="+stackName,
		"STACK_DIR="+deployDir,
		"STACK_DOMAIN="+stack.Domain,
		"COMPOSE_FILE="+composeFile,
		"COMPOSE_OVERRIDE_FILE="+filepath.Join(deployDir, "compose.override.yml"),
	)

	m.Logger.Info("running update script", "service", stackName, "script", scriptPath, "dir", deployDir)

	cmd := exec.CommandContext(ctx, "/bin/bash", "-euo", "pipefail", scriptPath)
	cmd.Dir = deployDir
	cmd.Env = env

	ring := newRingBuffer(50)

	// Use stream writer from UI if available (CLI mode), otherwise fall back to lineWriter (daemon mode).
	var outW, errW io.Writer
	if sw := m.ui().StreamWriter(); sw != nil {
		outW = sw
		errW = sw
	} else {
		outWriter := &lineWriter{logger: m.Logger, cmd: "update-script[" + stackName + "]", level: slog.LevelInfo, ring: ring}
		errWriter := &lineWriter{logger: m.Logger, cmd: "update-script[" + stackName + "]", level: slog.LevelWarn, ring: ring}
		outW = outWriter
		errW = errWriter
	}
	cmd.Stdout = outW
	cmd.Stderr = errW

	start := time.Now()
	runErr := cmd.Run()
	// Flush line-based writers.
	if lw, ok := outW.(*lineWriter); ok {
		lw.flush()
	}
	if lw, ok := errW.(*lineWriter); ok {
		lw.flush()
	}
	ui.FlushStreamWriter(m.ui())
	dur := time.Since(start).Round(time.Millisecond)

	if runErr != nil {
		exitCode := -1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		lastLines := strings.Join(ring.get(), "\n")
		if lastLines != "" {
			return fmt.Errorf("update script exited with code %d (duration: %s):\n%s", exitCode, dur, lastLines)
		}
		return fmt.Errorf("update script exited with code %d (duration: %s)", exitCode, dur)
	}

	m.Logger.Info("update script completed", "service", stackName, "duration", dur)
	return nil
}

// List returns info about all configured services, sorted by name.
func (m *ServiceManager) List() []ServiceInfo {
	names := slices.Sorted(maps.Keys(m.Config.Services))
	result := make([]ServiceInfo, 0, len(names))
	for _, name := range names {
		s := m.Config.Services[name]
		result = append(result, ServiceInfo{
			Name:         name,
			Domain:       s.Domain,
			AutoDeploy:   s.AutoDeploy,
			UpdateScript: s.UpdateScript,
		})
	}
	return result
}

// gitPull runs git pull in the IaC repo with auth for private repos.
func (m *ServiceManager) gitPull(ctx context.Context) error {
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
