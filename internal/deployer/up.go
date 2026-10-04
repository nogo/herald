package deployer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/runner"
	"github.com/nogo/herald/internal/ui"
)

// UpSpec is what Up needs to know about the stack beyond the instance itself.
type UpSpec struct {
	StackName      string            // names the main service when no service is explicit and none is named app
	Domain         string            // routed to the main service by Caddy
	ComposeFile    string            // absolute path
	EnvFile        string            // optional env_file from config, layered after the instance's .env
	DockerSecrets  map[string]string // mounted as docker secrets; their files must already exist
	Service        string            // explicit routing service; empty to detect
	Port           string            // explicit container port; empty to detect
	DefaultPort    string            // container port when the compose file exposes none
	InlineOverride string            // raw YAML deep-merged into the generated override
}

// Up is the last stage every deploy shares, production or preview: it writes
// the instance's compose override, ensures the caddy network, and runs
// docker compose up. The instance's source and .env must already be in place.
// u may be nil; without a UI stream, compose output goes to logger.
func Up(ctx context.Context, inst Instance, spec UpSpec, logger *slog.Logger, u ui.UI) error {
	if u == nil {
		u = ui.Nop()
	}
	if err := runStep(u, "Compose override", func() error { return writeOverride(inst, spec) }); err != nil {
		return err
	}
	if err := caddy.EnsureNetwork(ctx, logger); err != nil {
		return fmt.Errorf("ensuring caddy network: %w", err)
	}
	return runStep(u, "Compose up", func() error { return composeUp(ctx, inst, spec.ComposeFile, logger, u) })
}

// runStep reports fn to u as one named step.
func runStep(u ui.UI, name string, fn func() error) error {
	u.Step(name)
	if err := fn(); err != nil {
		u.StepFail(err)
		return err
	}
	u.StepDone("")
	return nil
}

func writeOverride(inst Instance, spec UpSpec) error {
	envFiles := []string{inst.EnvFile()}
	if spec.EnvFile != "" {
		envFiles = append(envFiles, spec.EnvFile)
	}
	data, err := GenerateOverride(OverrideParams{
		DeployDir:      inst.Dir,
		StackName:      spec.StackName,
		Domain:         spec.Domain,
		ComposeFile:    spec.ComposeFile,
		EnvFilePaths:   envFiles,
		DockerSecrets:  spec.DockerSecrets,
		Service:        spec.Service,
		Port:           spec.Port,
		DefaultPort:    spec.DefaultPort,
		InternalNet:    inst.InternalNetwork(),
		InlineOverride: spec.InlineOverride,
	})
	if err != nil {
		return fmt.Errorf("generating override: %w", err)
	}
	root, err := os.OpenRoot(inst.Dir)
	if err != nil {
		return fmt.Errorf("opening deploy root: %w", err)
	}
	defer root.Close()
	if err := root.WriteFile(filepath.Base(inst.OverrideFile()), data, 0644); err != nil {
		return fmt.Errorf("writing override: %w", err)
	}
	return nil
}

// composeUp executes docker compose up -d --build --remove-orphans.
func composeUp(ctx context.Context, inst Instance, composeFile string, logger *slog.Logger, u ui.UI) error {
	cctx := inst.composeContext(composeFile)
	logger.Info("compose up", "project", cctx.ProjectName)
	args := cctx.BaseArgs()
	args = append(args, "--progress", "plain", "up", "-d", "--build", "--remove-orphans")
	stream := u.StreamWriter()
	if stream == nil {
		// Daemon: no UI stream, so compose output goes to the log instead.
		return runner.RunCmd(ctx, logger, cctx.WorkDir, "docker", args...)
	}
	sw := &composeFilterWriter{w: stream}
	err := runner.RunCmdStream(ctx, logger, cctx.WorkDir, sw, sw, "docker", args...)
	sw.Flush()
	ui.FlushStreamWriter(u)
	return err
}
