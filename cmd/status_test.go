package cmd

import (
	"testing"

	"github.com/nogo/herald/internal/status"
)

func TestParseCPUPerc(t *testing.T) {
	cases := map[string]float64{
		"12.34%": 12.34,
		"0.00%":  0,
		" 5% ":   5,
		"bad":    0,
		"":       0,
	}
	for in, want := range cases {
		if got := parseCPUPerc(in); got != want {
			t.Errorf("parseCPUPerc(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseMemUsage(t *testing.T) {
	cases := map[string]int64{
		"100MiB / 1.9GiB": 100 << 20,
		"512B / 1GiB":     512,
		"2GiB / 4GiB":     2 << 30,
		"garbage":         0,
	}
	for in, want := range cases {
		if got := parseMemUsage(in); got != want {
			t.Errorf("parseMemUsage(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0B",
		512:     "512B",
		1 << 10: "1.0KiB",
		1 << 20: "1.0MiB",
		3 << 30: "3.0GiB",
	}
	for in, want := range cases {
		if got := humanizeBytes(in); got != want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusHasIssues(t *testing.T) {
	healthy := &status.ServerStatus{
		Caddy:  status.CaddyStatus{Running: true},
		Stacks: []status.StackStatus{{State: "running"}},
		Webhooks: []status.WebhookStatus{
			{Repo: "a/b", Registered: true},
		},
	}
	if statusHasIssues(healthy) {
		t.Error("healthy status reported issues")
	}

	tests := []struct {
		name string
		s    *status.ServerStatus
	}{
		{"caddy down", &status.ServerStatus{Caddy: status.CaddyStatus{Running: false}}},
		{"stack stopped", &status.ServerStatus{
			Caddy:  status.CaddyStatus{Running: true},
			Stacks: []status.StackStatus{{State: "stopped"}},
		}},
		{"webhook unregistered", &status.ServerStatus{
			Caddy:    status.CaddyStatus{Running: true},
			Webhooks: []status.WebhookStatus{{Repo: "a/b", Registered: false}},
		}},
		{"webhook unknown", &status.ServerStatus{
			Caddy:    status.CaddyStatus{Running: true},
			Webhooks: []status.WebhookStatus{{Repo: "a/b", Unknown: true}},
		}},
	}
	for _, tt := range tests {
		if !statusHasIssues(tt.s) {
			t.Errorf("%s: expected issues, got none", tt.name)
		}
	}
}
