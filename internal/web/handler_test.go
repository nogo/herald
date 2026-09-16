package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	h := NewWebHandler(collector, cfg, nil, slog.Default())
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

// newLiveTestHandler builds a WebHandler backed by an atomic.Pointer[config.Config]
// primed with initial, mirroring how cmd/serve.go publishes reloads.
func newLiveTestHandler(t *testing.T, initial *config.Config) (*WebHandler, *atomic.Pointer[config.Config]) {
	t.Helper()
	live := &atomic.Pointer[config.Config]{}
	live.Store(initial)
	collector := &status.StatusCollector{
		Config:     initial,
		LiveConfig: live,
		DataDir:    t.TempDir(),
		Logger:     slog.Default(),
	}
	h := NewWebHandler(collector, initial, live, slog.Default())
	if h == nil {
		t.Fatal("NewWebHandler returned nil")
	}
	return h, live
}

// TestBuildPublic_ReloadTogglesPublicOff verifies that a reload flipping
// availability.public from true to false hides the stack from the very next
// response, even though the cached status snapshot (s) never changes.
func TestBuildPublic_ReloadTogglesPublicOff(t *testing.T) {
	before := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	h, live := newLiveTestHandler(t, before)

	s := &status.ServerStatus{Stacks: []status.StackStatus{{Name: "blog", State: "running"}}}
	if pub := h.buildPublic(s); len(pub.Services) != 1 {
		t.Fatalf("before reload: expected blog visible, got %+v", pub.Services)
	}

	after := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: false}},
	}}
	live.Store(after)

	if pub := h.buildPublic(s); len(pub.Services) != 0 {
		t.Fatalf("after reload: expected blog hidden without restart, got %+v", pub.Services)
	}
}

// TestBuildPublic_ReloadTogglesPublicOn verifies the opposite direction: a
// stack that becomes public appears as soon as it is reflected in the status
// snapshot (i.e. on the next normal cache refresh), without any special-casing.
func TestBuildPublic_ReloadTogglesPublicOn(t *testing.T) {
	before := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: false}},
	}}
	h, live := newLiveTestHandler(t, before)

	s := &status.ServerStatus{Stacks: []status.StackStatus{{Name: "blog", State: "running"}}}
	if pub := h.buildPublic(s); len(pub.Services) != 0 {
		t.Fatalf("before reload: expected blog hidden, got %+v", pub.Services)
	}

	after := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	live.Store(after)

	if pub := h.buildPublic(s); len(pub.Services) != 1 || pub.Services[0].Name != "blog" {
		t.Fatalf("after reload: expected blog visible, got %+v", pub.Services)
	}
}

// TestBuildPublic_ReloadDropsRemovedStack verifies that a stack removed from
// config entirely (not just de-opted) disappears from public responses, even
// while the cached status snapshot still reports it.
func TestBuildPublic_ReloadDropsRemovedStack(t *testing.T) {
	before := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: true}},
		"old":  {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	h, live := newLiveTestHandler(t, before)

	s := &status.ServerStatus{Stacks: []status.StackStatus{
		{Name: "blog", State: "running"},
		{Name: "old", State: "running"},
	}}
	if pub := h.buildPublic(s); len(pub.Services) != 2 {
		t.Fatalf("before reload: expected both stacks visible, got %+v", pub.Services)
	}

	after := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	live.Store(after)

	pub := h.buildPublic(s)
	if len(pub.Services) != 1 || pub.Services[0].Name != "blog" {
		t.Fatalf("after reload: expected only blog visible, got %+v", pub.Services)
	}
}

// TestWebHandlerCfg_FallsBackWhenLiveConfigUnset verifies that an unset
// LiveConfig (mirroring a reload that never stored, e.g. because the reloaded
// config was invalid) falls back to the last snapshot the handler was
// constructed with, rather than panicking or exposing a zero-value config.
func TestWebHandlerCfg_FallsBackWhenLiveConfigUnset(t *testing.T) {
	cfg := &config.Config{Stacks: map[string]config.Stack{
		"blog": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	live := &atomic.Pointer[config.Config]{} // never stored - simulates no successful reload yet
	h := NewWebHandler(&status.StatusCollector{Config: cfg, DataDir: t.TempDir(), Logger: slog.Default()}, cfg, live, slog.Default())
	if h == nil {
		t.Fatal("NewWebHandler returned nil")
	}

	if got := h.cfg(); got != cfg {
		t.Fatalf("cfg() = %p, want fallback to startup config %p", got, cfg)
	}
}

// TestBuildPublic_ConcurrentReloadsRaceFree exercises buildPublic against a
// LiveConfig pointer that is swapped concurrently, to catch both data races
// (run with -race) and torn reads that mix stacks across two config
// generations within a single response.
func TestBuildPublic_ConcurrentReloadsRaceFree(t *testing.T) {
	cfgA := &config.Config{Stacks: map[string]config.Stack{
		"a": {Availability: &config.AvailabilityConfig{Public: true}},
		"b": {Availability: &config.AvailabilityConfig{Public: false}},
	}}
	cfgB := &config.Config{Stacks: map[string]config.Stack{
		"a": {Availability: &config.AvailabilityConfig{Public: false}},
		"b": {Availability: &config.AvailabilityConfig{Public: true}},
	}}
	h, live := newLiveTestHandler(t, cfgA)

	s := &status.ServerStatus{Stacks: []status.StackStatus{
		{Name: "a", State: "running"},
		{Name: "b", State: "running"},
	}}

	const iterations = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				live.Store(cfgA)
			} else {
				live.Store(cfgB)
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		pub := h.buildPublic(s)
		// cfgA and cfgB each expose exactly one of "a"/"b" as public, never
		// both and never neither - a mixed result would mean buildPublic read
		// the config inconsistently across stacks within one response.
		if len(pub.Services) != 1 {
			t.Fatalf("expected exactly one public stack per snapshot, got %+v", pub.Services)
		}
	}
	<-done
}
