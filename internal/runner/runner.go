// Package runner provides shared command execution helpers.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
)

// isTTY reports whether stdout is an interactive terminal.
func isTTY() bool {
	f, ok := any(os.Stdout).(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// RunCmd creates a command from name+args, optionally sets dir, and runs it via RunExecCmd.
func RunCmd(ctx context.Context, logger *slog.Logger, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return RunExecCmd(ctx, logger, cmd)
}

// RunExecCmd executes a pre-built command and returns an error on non-zero exit.
//
// When stdout is a TTY (interactive deploy), output streams directly to the
// terminal so progress is visible in real time.
//
// When stdout is not a TTY (daemon / journald), output is buffered and logged
// line-by-line: stdout at INFO, stderr at WARN on success or ERROR on failure.
func RunExecCmd(ctx context.Context, logger *slog.Logger, cmd *exec.Cmd) error {
	name := cmd.Path
	start := time.Now()

	if isTTY() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		dur := time.Since(start).Round(time.Millisecond)
		if runErr != nil {
			return fmt.Errorf("%s: %w (duration: %s)", name, runErr, dur)
		}
		logger.Info("command completed", "cmd", name, "duration", dur)
		return nil
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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
		stderrLevel := slog.LevelWarn
		if runErr != nil {
			stderrLevel = slog.LevelError
		}
		logLines(stderr.String(), stderrLevel)
	}

	if runErr != nil {
		return fmt.Errorf("%s: %w (duration: %s)", name, runErr, dur)
	}

	logger.Info("command completed", "cmd", name, "duration", dur)
	return nil
}
