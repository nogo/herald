package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nogo/herald/internal/config"
)

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "1m"},
		{2 * time.Minute, "2m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{3 * time.Hour, "3h"},
		{24 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		got := formatAge(tt.d)
		if got != tt.want {
			t.Errorf("formatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestParseComposePSOutput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cs, err := parseComposePSOutput([]byte(""))
		if err != nil || len(cs) != 0 {
			t.Errorf("got %v %v, want nil nil", cs, err)
		}
	})

	t.Run("empty array", func(t *testing.T) {
		cs, err := parseComposePSOutput([]byte("[]"))
		if err != nil || len(cs) != 0 {
			t.Errorf("got %v %v, want nil nil", cs, err)
		}
	})

	t.Run("json array single running", func(t *testing.T) {
		input := `[{"ID":"abc","Name":"herald-budget-app-1","State":"running"}]`
		cs, err := parseComposePSOutput([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 1 || cs[0].State != "running" {
			t.Errorf("got %v", cs)
		}
	})

	t.Run("json array mixed states", func(t *testing.T) {
		input := `[{"State":"running"},{"State":"exited"},{"State":"running"}]`
		cs, err := parseComposePSOutput([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 3 {
			t.Errorf("got %d containers, want 3", len(cs))
		}
	})

	t.Run("ndjson two containers", func(t *testing.T) {
		input := "{\"State\":\"running\"}\n{\"State\":\"exited\"}"
		cs, err := parseComposePSOutput([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 2 {
			t.Errorf("got %d containers, want 2", len(cs))
		}
		if cs[0].State != "running" || cs[1].State != "exited" {
			t.Errorf("unexpected states: %v", cs)
		}
	})

	t.Run("ndjson skips unparseable lines", func(t *testing.T) {
		input := "{\"State\":\"running\"}\nnot json\n{\"State\":\"exited\"}"
		cs, err := parseComposePSOutput([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 2 {
			t.Errorf("got %d containers, want 2", len(cs))
		}
	})
}

func TestComputeState(t *testing.T) {
	tests := []struct {
		name       string
		containers []containerPS
		wantUp     int
		wantTotal  int
		wantState  string
	}{
		{"no containers", nil, 0, 0, "stopped"},
		{"all running", []containerPS{{"running"}, {"running"}}, 2, 2, "running"},
		{"all exited", []containerPS{{"exited"}, {"exited"}}, 0, 2, "stopped"},
		{"degraded", []containerPS{{"running"}, {"exited"}}, 1, 2, "degraded"},
		{"single running", []containerPS{{"running"}}, 1, 1, "running"},
		{"running case insensitive", []containerPS{{"Running"}}, 1, 1, "running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, total, state := computeState(tt.containers)
			if up != tt.wantUp || total != tt.wantTotal || state != tt.wantState {
				t.Errorf("got up=%d total=%d state=%q, want up=%d total=%d state=%q",
					up, total, state, tt.wantUp, tt.wantTotal, tt.wantState)
			}
		})
	}
}

func TestCollectWebhookStatuses_MissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{
			Name:         "test",
			DeployDomain: "example.com",
			StacksDir:    dir,
		},
		Apps: map[string]config.App{
			"myapp": {Repo: "nogo/myapp", Branch: "main", Domain: "myapp.example.com"},
		},
	}
	c := &StatusCollector{Config: cfg, DataDir: dir}

	statuses, syncedAt := c.collectWebhookStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if !statuses[0].Unknown {
		t.Error("expected Unknown=true when webhooks.json missing")
	}
	if !syncedAt.IsZero() {
		t.Error("expected zero SyncedAt when webhooks.json missing")
	}
}

func TestCollectWebhookStatuses_WithFile(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	ws := &WebhookState{
		SyncedAt: ts,
		Repos: map[string]WebhookEntry{
			"nogo/myapp": {ID: 42, Registered: true},
		},
	}
	data, _ := json.MarshalIndent(ws, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "webhooks.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{
			Name:         "test",
			DeployDomain: "example.com",
			StacksDir:    dir,
		},
		Apps: map[string]config.App{
			"myapp": {Repo: "nogo/myapp", Branch: "main", Domain: "myapp.example.com"},
		},
	}
	c := &StatusCollector{Config: cfg, DataDir: dir}

	statuses, syncedAt := c.collectWebhookStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if !statuses[0].Registered {
		t.Error("expected Registered=true")
	}
	if statuses[0].ID != 42 {
		t.Errorf("got ID=%d, want 42", statuses[0].ID)
	}
	if !syncedAt.Equal(ts) {
		t.Errorf("got syncedAt=%v, want %v", syncedAt, ts)
	}
}

func TestCollectWebhookStatuses_UnregisteredRepo(t *testing.T) {
	dir := t.TempDir()
	ws := &WebhookState{
		SyncedAt: time.Now().UTC(),
		Repos:    map[string]WebhookEntry{},
	}
	data, _ := json.MarshalIndent(ws, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "webhooks.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.Server{
			Name:         "test",
			DeployDomain: "example.com",
			StacksDir:    dir,
		},
		Apps: map[string]config.App{
			"myapp": {Repo: "nogo/myapp", Branch: "main", Domain: "myapp.example.com"},
		},
	}
	c := &StatusCollector{Config: cfg, DataDir: dir}

	statuses, _ := c.collectWebhookStatuses()
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Registered {
		t.Error("expected Registered=false for unknown repo")
	}
	if statuses[0].Unknown {
		t.Error("expected Unknown=false when file exists but repo not in it")
	}
}

func TestSaveWebhookState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webhooks.json")

	ts := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	ws := &WebhookState{
		SyncedAt: ts,
		Repos: map[string]WebhookEntry{
			"nogo/app1": {ID: 1, Registered: true},
		},
	}

	if err := SaveWebhookState(path, ws); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var loaded WebhookState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if len(loaded.Repos) != 1 {
		t.Errorf("got %d repos, want 1", len(loaded.Repos))
	}
	if loaded.Repos["nogo/app1"].ID != 1 {
		t.Error("expected nogo/app1 to have ID 1")
	}
	if !loaded.SyncedAt.Equal(ts) {
		t.Errorf("got SyncedAt=%v, want %v", loaded.SyncedAt, ts)
	}
}
