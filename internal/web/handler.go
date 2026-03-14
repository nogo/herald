package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/status"
)

//go:embed templates static
var content embed.FS

type cachedStatus struct {
	mu      sync.Mutex
	data    *status.ServerStatus
	fetched time.Time
	ttl     time.Duration
}

func (c *cachedStatus) Get(ctx context.Context, collector *status.StatusCollector) (*status.ServerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil && time.Since(c.fetched) < c.ttl {
		return c.data, nil
	}
	s, err := collector.Collect(ctx)
	if err != nil {
		return nil, err
	}
	c.data = s
	c.fetched = time.Now()
	return s, nil
}

// WebHandler serves the status page web UI.
type WebHandler struct {
	Collector *status.StatusCollector
	Config    *config.Config
	Templates *template.Template
	Password  string
	Logger    *slog.Logger
	cache     cachedStatus
}

var tmplFuncs = template.FuncMap{
	"stateClass": func(state string) string {
		switch state {
		case "running":
			return "running"
		case "degraded":
			return "degraded"
		case "error":
			return "error"
		default:
			return "stopped"
		}
	},
	"containers": func(up, total int) string {
		if total == 0 {
			return "—"
		}
		return fmt.Sprintf("%d/%d", up, total)
	},
	"domainURL": func(domain string) string {
		return "https://" + domain
	},
}

// NewWebHandler creates a WebHandler. Returns nil if template parsing fails.
func NewWebHandler(collector *status.StatusCollector, cfg *config.Config, password string, logger *slog.Logger) *WebHandler {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(content, "templates/*.html")
	if err != nil {
		logger.Error("failed to parse templates, status page disabled", "error", err)
		return nil
	}
	return &WebHandler{
		Collector: collector,
		Config:    cfg,
		Templates: tmpl,
		Password:  password,
		Logger:    logger,
		cache:     cachedStatus{ttl: 5 * time.Second},
	}
}

// RegisterRoutes adds the status page routes to mux.
func (h *WebHandler) RegisterRoutes(mux *http.ServeMux) {
	cop := &http.CrossOriginProtection{}
	authed := func(fn http.HandlerFunc) http.Handler {
		return cop.Handler(h.basicAuth(fn))
	}
	mux.Handle("GET /{$}", authed(h.handleStatus))
	mux.Handle("GET /app/{name}", authed(h.handleApp))
	mux.Handle("GET /api/status", authed(h.handleAPIStatus))
	mux.Handle("GET /static/", http.FileServerFS(content))
}

func (h *WebHandler) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		// Constant-time comparison prevents timing attacks on credentials.
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte("herald")) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(h.Password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="Herald"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *WebHandler) getStatus(r *http.Request) (*status.ServerStatus, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	return h.cache.Get(ctx, h.Collector)
}

type statusData struct {
	*status.ServerStatus
	CollectedAt time.Time
}

type appData struct {
	*status.ServerStatus
	CollectedAt time.Time
	AppName     string
	AppConfig   config.App
	AppStatus   *status.AppStatus
}

func (h *WebHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	s, err := h.getStatus(r)
	if err != nil {
		h.Logger.Error("collecting status", "error", err)
		http.Error(w, "Cannot connect to Docker: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := statusData{ServerStatus: s, CollectedAt: time.Now()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "status", data); err != nil {
		h.Logger.Error("executing status template", "error", err)
	}
}

func (h *WebHandler) handleApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := h.Config.Apps[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s, err := h.getStatus(r)
	if err != nil {
		h.Logger.Error("collecting status", "error", err)
		http.Error(w, "Cannot connect to Docker: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var appSt *status.AppStatus
	for i := range s.Apps {
		if s.Apps[i].Name == name {
			appSt = &s.Apps[i]
			break
		}
	}
	data := appData{
		ServerStatus: s,
		CollectedAt:  time.Now(),
		AppName:      name,
		AppConfig:    app,
		AppStatus:    appSt,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "app", data); err != nil {
		h.Logger.Error("executing app template", "error", err)
	}
}

func (h *WebHandler) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	s, err := h.getStatus(r)
	if err != nil {
		h.Logger.Error("collecting status for API", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s) //nolint:errcheck
}
