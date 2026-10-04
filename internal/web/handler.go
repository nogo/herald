package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nogo/herald/internal/availability"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/status"
)

//go:embed templates
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

// WebHandler serves the public availability page. It is a single, unauthenticated
// endpoint that exposes only what is safe on the open internet: an overall status,
// the up/degraded/down state of stacks that opted in via availability.public, and a
// last-checked time. Operational inventory (repos, refs, secrets, webhooks, paths)
// lives in the CLI (herald status / herald doctor), never here.
type WebHandler struct {
	Collector *status.StatusCollector
	Config    *config.Live
	Templates *template.Template
	Logger    *slog.Logger
	cache     cachedStatus
}

// NewWebHandler creates a WebHandler. Returns nil if template parsing fails.
func NewWebHandler(collector *status.StatusCollector, cfg *config.Live, logger *slog.Logger) *WebHandler {
	tmpl, err := template.New("").ParseFS(content, "templates/*.html")
	if err != nil {
		logger.Error("failed to parse templates, status page disabled", "error", err)
		return nil
	}
	return &WebHandler{
		Collector: collector,
		Config:    cfg,
		Templates: tmpl,
		Logger:    logger,
		cache:     cachedStatus{ttl: 5 * time.Second},
	}
}

// RegisterRoutes adds the single public status route. The page is fully
// self-contained (CSS is inlined), so there are no static assets to serve.
func (h *WebHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.handleStatus)
}

// publicDay is one bar of a stack's history. Title is the hover text: the date and
// the minutes degraded or down, never the time of a state change.
type publicDay struct {
	State string // "up", "degraded", "down", or "nodata"
	Title string
}

// publicService is the public-safe view of one opted-in stack.
type publicService struct {
	Name   string
	State  string // "up", "degraded", or "down"
	Uptime string // e.g. "99.95%"; "" without any data
	Days   []publicDay
}

// publicStatus is the entire public page data. It deliberately carries no domain,
// server name, source, ref, commit, preview, or webhook information.
type publicStatus struct {
	Overall   string // "operational", "degraded" or "down"; "" when no stack is public
	Services  []publicService
	UpdatedAt time.Time
}

func (h *WebHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	s, err := h.cache.Get(ctx, h.Collector)
	if err != nil {
		h.Logger.Error("collecting status", "error", err)
		http.Error(w, "Service temporarily unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "status", h.buildPublic(s)); err != nil {
		h.Logger.Error("executing status template", "error", err)
	}
}

// buildPublic reduces the full collected status to the public-safe view, keeping
// only stacks that opted in with availability.public.
func (h *WebHandler) buildPublic(s *status.ServerStatus) publicStatus {
	cfg := h.Config.Load()
	now := time.Now()
	var services []publicService
	up, total := 0, 0
	for _, st := range s.Stacks {
		stackCfg, ok := cfg.Stacks[st.Name]
		if !ok || stackCfg.Availability == nil || !stackCfg.Availability.Public {
			continue
		}
		state := availability.PublicState(st.State)
		services = append(services, h.publicService(st.Name, state, now))
		total++
		if state == "up" {
			up++
		}
	}

	overall := ""
	switch {
	case total == 0:
	case up == total:
		overall = "operational"
	case up == 0:
		overall = "down"
	default:
		overall = "degraded"
	}

	return publicStatus{Overall: overall, Services: services, UpdatedAt: now}
}

// publicService builds one stack's view: its current state and day bars. A
// history that cannot be read renders as no data; the current state still shows.
func (h *WebHandler) publicService(name, state string, now time.Time) publicService {
	svc := publicService{Name: name, State: state}
	days, err := availability.NewLog(h.Collector.DataDir).History(name, now)
	if err != nil {
		h.Logger.Warn("reading availability history", "stack", name, "error", err)
	}
	if pct, ok := availability.Uptime(days); ok {
		svc.Uptime = fmt.Sprintf("%.2f%%", pct)
	}
	for _, d := range days {
		date := d.Date.Format("2006-01-02")
		title := date + ": no data"
		if d.State != availability.NoData {
			title = fmt.Sprintf("%s: %d min degraded or down", date, d.Minutes)
		}
		svc.Days = append(svc.Days, publicDay{State: d.State, Title: title})
	}
	return svc
}
