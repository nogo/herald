package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/status"
)

func newTestHandler(t *testing.T, cfg *config.Config) *WebHandler {
	t.Helper()
	collector := &status.StatusCollector{
		Config:  cfg,
		DataDir: t.TempDir(),
		Logger:  slog.Default(),
	}
	h := NewWebHandler(collector, cfg, slog.Default())
	if h == nil {
		t.Fatal("NewWebHandler returned nil")
	}
	return h
}

func TestNewWebHandler_ParsesTemplates(t *testing.T) {
	h := newTestHandler(t, &config.Config{Stacks: map[string]config.Stack{}})
	if h.Templates == nil {
		t.Fatal("Templates is nil")
	}
}

func TestHandleStatus_PublicNoAuth(t *testing.T) {
	h := newTestHandler(t, &config.Config{Stacks: map[string]config.Stack{}})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// No credentials — the page must be served publicly.
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("empty response body")
	}
}

func TestOperationalEndpointsRemoved(t *testing.T) {
	h := newTestHandler(t, &config.Config{Stacks: map[string]config.Stack{}})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, path := range []string{"/app/myapp", "/api/status"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s served (%d); operational endpoints must not exist", path, w.Code)
		}
	}
}

func TestBuildPublic_OnlyOptInStacksAndSafeData(t *testing.T) {
	cfg := &config.Config{Stacks: map[string]config.Stack{
		"blog":    {Repo: "me/blog", Domain: "blog.example.com", Availability: &config.AvailabilityConfig{Public: true}},
		"private": {Repo: "me/private", Domain: "secret.example.com"}, // not public
	}}
	h := newTestHandler(t, cfg)

	s := &status.ServerStatus{Stacks: []status.StackStatus{
		{Name: "blog", Domain: "blog.example.com", State: "running", DeployedRef: "main@abc123"},
		{Name: "private", Domain: "secret.example.com", State: "running"},
	}}
	pub := h.buildPublic(s)

	if len(pub.Services) != 1 || pub.Services[0].Name != "blog" {
		t.Fatalf("expected only opt-in 'blog', got %+v", pub.Services)
	}
	if pub.Services[0].State != "up" {
		t.Errorf("running stack should be 'up', got %q", pub.Services[0].State)
	}
	if pub.Overall != "operational" {
		t.Errorf("overall = %q, want operational", pub.Overall)
	}

	// Render must not leak the private stack, domains, or refs.
	var sb strings.Builder
	if err := h.Templates.ExecuteTemplate(&sb, "status", pub); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, leak := range []string{"private", "secret.example.com", "blog.example.com", "abc123", "me/blog"} {
		if strings.Contains(out, leak) {
			t.Errorf("public page leaked %q\n---\n%s", leak, out)
		}
	}
}

func TestBuildPublic_OverallStates(t *testing.T) {
	cfg := &config.Config{Stacks: map[string]config.Stack{
		"a": {Availability: &config.AvailabilityConfig{Public: true}},
		"b": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	h := newTestHandler(t, cfg)

	cases := []struct {
		name    string
		states  map[string]string
		overall string
	}{
		{"all up", map[string]string{"a": "running", "b": "running"}, "operational"},
		{"some down", map[string]string{"a": "running", "b": "stopped"}, "degraded"},
		{"all down", map[string]string{"a": "stopped", "b": "error"}, "down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &status.ServerStatus{Stacks: []status.StackStatus{
				{Name: "a", State: tc.states["a"]},
				{Name: "b", State: tc.states["b"]},
			}}
			if got := h.buildPublic(s).Overall; got != tc.overall {
				t.Errorf("overall = %q, want %q", got, tc.overall)
			}
		})
	}
}

func TestBuildPublic_NoPublicStacksIsUnknown(t *testing.T) {
	cfg := &config.Config{Stacks: map[string]config.Stack{"a": {}}}
	h := newTestHandler(t, cfg)
	s := &status.ServerStatus{Stacks: []status.StackStatus{{Name: "a", State: "running"}}}
	if got := h.buildPublic(s).Overall; got != "unknown" {
		t.Errorf("overall = %q, want unknown", got)
	}
}

func TestCachedStatus_TTL(t *testing.T) {
	c := &cachedStatus{ttl: 5}
	if c.data != nil {
		t.Fatal("expected nil initial data")
	}
	c.data = &status.ServerStatus{ServerName: "cached"}
	if c.ttl != 5 {
		t.Errorf("ttl not set correctly")
	}
}
