// Package runner provides shared command execution helpers.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// RunCmd creates a command from name+args, optionally sets dir, and runs it via RunExecCmd.
func RunCmd(ctx context.Context, logger *slog.Logger, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return RunExecCmd(ctx, logger, cmd)
}

// RunExecCmd executes a pre-built command, logs stdout/stderr line-by-line, and returns an
// error on non-zero exit. Use this when the command needs custom Env or other settings.
func RunExecCmd(ctx context.Context, logger *slog.Logger, cmd *exec.Cmd) error {
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
