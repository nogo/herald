// Package availability keeps the public up/degraded/down history of opted-in
// stacks. Per stack it appends to <data_dir>/availability/<stack>.jsonl one line
// per state change plus one heartbeat line per UTC day, and turns those lines into
// per-day buckets for the public status page. The files are the one Herald record
// that cannot be rebuilt from git or Docker; deleting them loses history only.
package availability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/status"
)

// Days is the length of the history shown on the public page.
const Days = 90

// The public vocabulary for a stack's state, plus NoData for a day without records.
const (
	Up       = "up"
	Degraded = "degraded"
	Down     = "down"
	NoData   = "nodata"
)

// PublicState maps an internal stack state to the public vocabulary. Anything
// that is not clearly running or degraded is reported as down.
func PublicState(state string) string {
	switch state {
	case "running":
		return Up
	case "degraded":
		return Degraded
	default: // stopped, error, not deployed
		return Down
	}
}

type entry struct {
	Time  time.Time `json:"t"`
	State string    `json:"s"`
}

// Log is the on-disk availability history under one directory.
type Log struct {
	Dir string
}

// NewLog returns the Log stored under dataDir.
func NewLog(dataDir string) Log {
	return Log{Dir: filepath.Join(dataDir, "availability")}
}

// path is safe to build from a stack name: config restricts names to [a-z0-9_-].
func (l Log) path(stack string) string { return filepath.Join(l.Dir, stack+".jsonl") }

// Record appends a line for stack when its state differs from the last recorded
// one, or when no line has been written yet on now's UTC day (the heartbeat).
func (l Log) Record(stack, state string, now time.Time) error {
	entries, err := l.read(stack)
	if err != nil {
		return err
	}
	if n := len(entries); n > 0 {
		last := entries[n-1]
		if last.State == state && sameDay(last.Time, now) {
			return nil
		}
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path(stack), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(entry{Time: now.UTC().Truncate(time.Second), State: state})
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// read returns the entries of stack in file order. A missing file is no history;
// unreadable lines are skipped so one torn write cannot hide the rest.
func (l Log) read(stack string) ([]entry, error) {
	f, err := os.Open(l.path(stack))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.Time.IsZero() {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Day is one bar of the history. Date is the UTC day; Minutes counts the minutes
// spent degraded or down; Covered counts the minutes of the day with a record.
type Day struct {
	Date    time.Time
	State   string // Up, Degraded, Down or NoData
	Minutes int
	Covered int
}

// History returns the last Days days of stack ending with now's UTC day, oldest
// first. A recorded state holds until the next line of the same day, and from the
// last line of a day to its end (or to now, for today). A day without lines is
// NoData, never Up. On a read error it still returns the days seen so far,
// all NoData if nothing was readable, with the error.
func (l Log) History(stack string, now time.Time) ([]Day, error) {
	entries, err := l.read(stack)
	now = now.UTC()
	today := now.Truncate(24 * time.Hour)
	days := make([]Day, Days)
	for i := range days {
		days[i] = Day{Date: today.AddDate(0, 0, i-Days+1), State: NoData}
	}
	for i, e := range entries {
		t := e.Time.UTC()
		idx := int(t.Truncate(24*time.Hour).Sub(days[0].Date) / (24 * time.Hour))
		if idx < 0 || idx >= Days || t.After(now) {
			continue
		}
		d := &days[idx]
		end := d.Date.AddDate(0, 0, 1)
		if end.After(now) {
			end = now
		}
		if i+1 < len(entries) && entries[i+1].Time.Before(end) {
			end = entries[i+1].Time.UTC()
		}
		mins := max(int(end.Sub(t)/time.Minute), 0)
		d.Covered += mins
		if e.State != Up {
			d.Minutes += mins
		}
		// A day is as bad as its worst state, however short.
		d.State = worst(d.State, e.State)
	}
	return days, err
}

func worst(a, b string) string {
	rank := map[string]int{NoData: 0, Up: 1, Degraded: 2, Down: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Uptime is the share of recorded minutes spent up, as a percentage. ok is false
// when no day has data.
func Uptime(days []Day) (pct float64, ok bool) {
	covered, bad := 0, 0
	for _, d := range days {
		covered += d.Covered
		bad += d.Minutes
	}
	if covered == 0 {
		return 0, false
	}
	return 100 * float64(covered-bad) / float64(covered), true
}

// Run samples the public stacks every interval until ctx ends and records each
// one's state, starting immediately.
func (l Log) Run(ctx context.Context, interval time.Duration, cfg *config.Live, collect func(context.Context) (*status.ServerStatus, error), logger *slog.Logger) {
	sample := func() {
		cctx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		s, err := collect(cctx)
		if err != nil {
			logger.Warn("availability: collecting status", "error", err)
			return
		}
		now := time.Now()
		stacks := cfg.Load().Stacks
		for _, st := range s.Stacks {
			sc, ok := stacks[st.Name]
			if !ok || sc.Availability == nil || !sc.Availability.Public {
				continue
			}
			if err := l.Record(st.Name, PublicState(st.State), now); err != nil {
				logger.Warn("availability: recording state", "stack", st.Name, "error", err)
			}
		}
	}
	sample()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample()
		}
	}
}

func sameDay(a, b time.Time) bool {
	a, b = a.UTC(), b.UTC()
	return a.Truncate(24 * time.Hour).Equal(b.Truncate(24 * time.Hour))
}
