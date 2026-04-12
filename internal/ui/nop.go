package ui

import (
	"io"
	"time"
)

type nop struct{}

// Nop returns a UI that does nothing. Used in daemon mode.
func Nop() UI { return nop{} }

func (nop) Step(string)                       {}
func (nop) StepDone(string)                   {}
func (nop) StepFail(error)                    {}
func (nop) StreamWriter() io.Writer           { return nil }
func (nop) Done(string, error, time.Duration) {}
