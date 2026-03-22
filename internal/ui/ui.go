// Package ui provides deploy progress reporting for CLI and daemon contexts.
package ui

import (
	"io"
	"time"
)

// UI observes deploy/operation step progression.
// Implementations must be safe for sequential (not concurrent) use.
type UI interface {
	// Step marks the beginning of a named step.
	Step(name string)
	// StepDone marks the current step as successful. Detail is optional context (e.g. "1 env key").
	StepDone(detail string)
	// StepFail marks the current step as failed.
	StepFail(err error)
	// StreamWriter returns a writer for long-running command output (e.g. docker compose).
	// Returns nil if output should be handled by the caller's default mechanism.
	StreamWriter() io.Writer
	// Done prints a final summary line.
	Done(name string, err error, dur time.Duration)
}
