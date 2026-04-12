package ui

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// ANSI escape codes.
const (
	reset = "\033[0m"
	dim   = "\033[2m"
	green = "\033[32m"
	red   = "\033[31m"
)

// ttyUI renders step-based progress to an interactive terminal.
type ttyUI struct {
	w         io.Writer
	stepName  string
	stepStart time.Time
	stream    *prefixWriter
}

// NewTTY returns a UI that writes human-friendly step output to w.
func NewTTY(w io.Writer) UI {
	return &ttyUI{w: w}
}

func (t *ttyUI) Step(name string) {
	t.stepName = name
	t.stepStart = time.Now()
	fmt.Fprintf(t.w, "  → %s\n", name)
}

func (t *ttyUI) StepDone(detail string) {
	dur := fmtDuration(time.Since(t.stepStart))
	if detail != "" {
		fmt.Fprintf(t.w, "  %s✓%s %s %s(%s, %s)%s\n", green, reset, t.stepName, dim, detail, dur, reset)
	} else {
		fmt.Fprintf(t.w, "  %s✓%s %s %s(%s)%s\n", green, reset, t.stepName, dim, dur, reset)
	}
}

func (t *ttyUI) StepFail(err error) {
	dur := fmtDuration(time.Since(t.stepStart))
	fmt.Fprintf(t.w, "  %s✗%s %s %s(%s)%s\n", red, reset, t.stepName, dim, dur, reset)
	if err != nil {
		fmt.Fprintf(t.w, "    %s%s%s\n", red, err, reset)
	}
}

func (t *ttyUI) StreamWriter() io.Writer {
	if t.stream == nil {
		t.stream = &prefixWriter{w: t.w, prefix: dim + "      " + reset}
	}
	return t.stream
}

func (t *ttyUI) Done(name string, err error, dur time.Duration) {
	d := fmtDuration(dur)
	if err != nil {
		fmt.Fprintf(t.w, "\n%s✗ %s failed%s %s(%s)%s\n", red, name, reset, dim, d, reset)
	} else {
		fmt.Fprintf(t.w, "\n%s✓ %s complete%s %s(%s)%s\n", green, name, reset, dim, d, reset)
	}
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// prefixWriter writes each line with a prefix, used for streaming docker output.
type prefixWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	pw.buf = append(pw.buf, p...)
	for {
		idx := strings.IndexByte(string(pw.buf), '\n')
		if idx < 0 {
			break
		}
		line := string(pw.buf[:idx])
		pw.buf = pw.buf[idx+1:]
		if _, err := fmt.Fprintf(pw.w, "%s%s%s\n", pw.prefix, line, reset); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Flush writes any remaining buffered content.
func (pw *prefixWriter) Flush() {
	if len(pw.buf) > 0 {
		fmt.Fprintf(pw.w, "%s%s%s\n", pw.prefix, string(pw.buf), reset)
		pw.buf = pw.buf[:0]
	}
}

// FlushStreamWriter flushes the stream writer if the UI is a ttyUI with buffered content.
func FlushStreamWriter(u UI) {
	if t, ok := u.(*ttyUI); ok && t.stream != nil {
		t.stream.Flush()
	}
}
