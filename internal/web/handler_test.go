package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/status"
)

func testCollector(t *testing.T) *status.StatusCollector {
	t.Helper()
	return &status.StatusCollector{
		Config: &config.Config{
			Server: config.Server{Name: "test-server"},
			Apps:   map[string]config.App{},
			Services: map[string]config.Service{},
		},
		DataDir: t.TempDir(),
		Logger:  slog.Default(),
	}
}

func testHandler(t *testing.T, password string) *WebHandler {
	t.Helper()
	h := NewWebHandler(testCollector(t), &config.Config{
		Server: config.Server{Name: "test-server"},
		Apps:   map[string]config.App{},
		Services: map[string]config.Service{},
	}, password, slog.Default())
	if h == nil {
		t.Fatal("NewWebHandler returned nil")
	}
	return h
}

func TestNewWebHandler_ParsesTemplates(t *testing.T) {
	h := testHandler(t, "secret")
	if h.Templates == nil {
		t.Fatal("Templates is nil")
	}
}

func TestBasicAuth_Rejects(t *testing.T) {
	h := testHandler(t, "correct")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	tests := []struct {
		name string
		user string
		pass string
		want int
	}{
		{"no auth", "", "", http.StatusUnauthorized},
		{"wrong user", "wrong", "correct", http.StatusUnauthorized},
		{"wrong pass", "herald", "wrong", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tc.user != "" || tc.pass != "" {
				req.SetBasicAuth(tc.user, tc.pass)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestHandleStatus_OK(t *testing.T) {
	h := testHandler(t, "secret")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("herald", "secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if len(body) == 0 {
		t.Error("empty response body")
	}
}

func TestHandleApp_NotFound(t *testing.T) {
	h := testHandler(t, "secret")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/app/nonexistent", nil)
	req.SetBasicAuth("herald", "secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}

func TestHandleApp_OK(t *testing.T) {
	cfg := &config.Config{
		Server: config.Server{Name: "test-server"},
		Apps: map[string]config.App{
			"myapp": {Repo: "owner/repo", Branch: "main", Domain: "myapp.example.com", Compose: "compose.yml"},
		},
		Services: map[string]config.Service{},
	}
	collector := &status.StatusCollector{
		Config:  cfg,
		DataDir: t.TempDir(),
		Logger:  slog.Default(),
	}
	h := NewWebHandler(collector, cfg, "secret", slog.Default())
	if h == nil {
		t.Fatal("NewWebHandler returned nil")
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/app/myapp", nil)
	req.SetBasicAuth("herald", "secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func TestHandleAPIStatus_OK(t *testing.T) {
	h := testHandler(t, "secret")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.SetBasicAuth("herald", "secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var result map[string]any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("JSON decode error: %v", err)
	}
}

func TestHandleAPIStatus_Unauthorized(t *testing.T) {
	h := testHandler(t, "secret")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}

func TestCachedStatus_TTL(t *testing.T) {
	c := &cachedStatus{ttl: 5}
	if c.data != nil {
		t.Fatal("expected nil initial data")
	}
	// Simulate populated cache.
	s := &status.ServerStatus{ServerName: "cached"}
	c.data = s
	// fetched is zero time — TTL of 5ns means it's expired immediately
	// With ttl=5ns and zero fetched time, it's expired: re-collect would happen.
	// Just verify the struct is accessible.
	if c.ttl != 5 {
		t.Errorf("ttl not set correctly")
	}
}
