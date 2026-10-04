package availability

import (
	"os"
	"strings"
	"testing"
	"time"
)

var noon = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func lines(t *testing.T, l Log, stack string) []string {
	t.Helper()
	b, err := os.ReadFile(l.path(stack))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestRecord_ChangesAndDailyHeartbeat(t *testing.T) {
	l := NewLog(t.TempDir())
	rec := func(state string, at time.Time) {
		t.Helper()
		if err := l.Record("blog", state, at); err != nil {
			t.Fatal(err)
		}
	}
	rec(Up, noon)                      // first line
	rec(Up, noon.Add(time.Minute))     // unchanged, same day: nothing
	rec(Down, noon.Add(2*time.Minute)) // change
	rec(Down, noon.Add(3*time.Minute)) // unchanged
	rec(Down, noon.Add(24*time.Hour))  // next day: heartbeat
	rec(Down, noon.Add(25*time.Hour))  // same day again: nothing
	if got := lines(t, l, "blog"); len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(got), got)
	}
}

func TestHistory_NoFileIsAllNoData(t *testing.T) {
	days, err := NewLog(t.TempDir()).History("blog", noon)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != Days {
		t.Fatalf("got %d days, want %d", len(days), Days)
	}
	for _, d := range days {
		if d.State != NoData {
			t.Fatalf("%v is %q, want nodata", d.Date, d.State)
		}
	}
	if _, ok := Uptime(days); ok {
		t.Error("uptime reported without data")
	}
}

func TestHistory_BucketsAndGaps(t *testing.T) {
	l := NewLog(t.TempDir())
	day := func(n int, h int) time.Time { return time.Date(2026, 10, 4-n, h, 0, 0, 0, time.UTC) }
	for _, e := range []struct {
		at    time.Time
		state string
	}{
		{day(3, 0), Up},
		{day(2, 6), Up}, // heartbeat; day 3 ends, day 2 has up only from 06:00
		{day(2, 8), Degraded},
		{day(2, 9), Up},
		// day 1: Herald not running, no lines
		{day(0, 0), Down},
	} {
		if err := l.Record("blog", e.state, e.at); err != nil {
			t.Fatal(err)
		}
	}
	days, err := l.History("blog", noon)
	if err != nil {
		t.Fatal(err)
	}
	last := len(days) - 1
	want := []struct {
		idx     int
		state   string
		minutes int
		covered int
	}{
		{last - 4, NoData, 0, 0},
		{last - 3, Up, 0, 24 * 60},
		{last - 2, Degraded, 60, 18 * 60},
		{last - 1, NoData, 0, 0}, // the gap is not "up"
		{last, Down, 12 * 60, 12 * 60},
	}
	for _, w := range want {
		d := days[w.idx]
		if d.State != w.state || d.Minutes != w.minutes || d.Covered != w.covered {
			t.Errorf("day -%d = %+v, want %+v", last-w.idx, d, w)
		}
	}
	pct, ok := Uptime(days)
	if !ok || pct < 100*float64(24*60+17*60)/float64(24*60+18*60+12*60)-0.001 || pct > 100*float64(24*60+17*60)/float64(24*60+18*60+12*60)+0.001 {
		t.Errorf("uptime = %v", pct)
	}
}

func TestHistory_IgnoresOldAndTornLines(t *testing.T) {
	l := NewLog(t.TempDir())
	if err := l.Record("blog", Down, noon.AddDate(0, 0, -Days-5)); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(l.path("blog"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{\"t\":\"2026-10\n")
	f.Close()
	days, err := l.History("blog", noon)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		if d.State != NoData {
			t.Fatalf("%v is %q", d.Date, d.State)
		}
	}
}

func TestPublicState(t *testing.T) {
	for in, want := range map[string]string{"running": Up, "degraded": Degraded, "stopped": Down, "error": Down, "not deployed": Down} {
		if got := PublicState(in); got != want {
			t.Errorf("PublicState(%q) = %q, want %q", in, got, want)
		}
	}
}
