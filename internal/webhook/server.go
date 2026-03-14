package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/web"
)

const maxBodySize = 10 << 20 // 10 MB

// DeployRequest carries the information needed to trigger a deploy.
type DeployRequest struct {
	AppName  string
	App      config.App
	Repo     string
	Branch   string
	Commit   string
	CloneURL string
}

// Server is the webhook HTTP server.
type Server struct {
	Config            *config.Config
	Secret            string
	Verbose           bool
	Web               *web.WebHandler              // optional status page handler; nil disables status page
	OnDeploy          func(DeployRequest)          // called for each matched app; must be non-nil
	IaCRepo           string                       // GitHub full name of the server IaC repo, e.g. "nogo/srv2"
	OnIaCPush         func()                       // called when a push to IaCRepo is received; may be nil
	OnPreviewDeploy   func(appName, branch, commit string) // called for preview-enabled apps on non-default branches
	OnPreviewTeardown func(appName, branch string)         // called when a preview branch is deleted or PR closed
}

// Handler returns the configured ServeMux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /health", s.handleHealth)
	if s.Web != nil {
		s.Web.RegisterRoutes(mux)
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("reading webhook body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")

	if !s.verifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
		slog.Info("webhook rejected",
			"event", eventType,
			"result", "rejected: invalid signature",
		)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid signature"})
		return
	}

	if eventType == "ping" {
		slog.Info("webhook", "event", "ping", "result", "pong")
		writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
		return
	}

	var repo, branch, commit, cloneURL string
	var isDelete bool   // true when a push event deletes a branch
	var prAction string // pull_request action: "opened", "closed", "synchronize"

	switch eventType {
	case "push":
		var p PushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			slog.Error("parsing push payload", "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
			return
		}
		repo = p.Repository.FullName
		branch = strings.TrimPrefix(p.Ref, "refs/heads/")
		commit = p.After
		cloneURL = p.Repository.CloneURL
		isDelete = p.Deleted || strings.HasPrefix(p.After, "000000")

	case "pull_request":
		var p PullRequestPayload
		if err := json.Unmarshal(body, &p); err != nil {
			slog.Error("parsing pull_request payload", "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
			return
		}
		repo = p.Repository.FullName
		branch = p.PullRequest.Head.Ref
		commit = p.PullRequest.Head.SHA
		cloneURL = p.Repository.CloneURL
		prAction = p.Action

	default:
		slog.Info("webhook", "event", eventType, "result", "event ignored")
		writeJSON(w, http.StatusOK, map[string]string{"message": "event ignored"})
		return
	}

	if s.Verbose {
		slog.Debug("webhook payload", "event", eventType, "repo", repo, "branch", branch, "body", string(body))
	}

	var matchedNames []string
	for name, app := range s.Config.Apps {
		if app.Repo == repo && app.Branch == branch {
			matchedNames = append(matchedNames, name)
		}
	}

	// Check for IaC repo push (stacks auto-deploy).
	iacPush := s.IaCRepo != "" && repo == s.IaCRepo && s.OnIaCPush != nil
	if eventType == "push" && iacPush {
		slog.Info("webhook",
			"event", eventType,
			"repo", repo,
			"result", "accepted: IaC repo push",
		)
		go s.OnIaCPush()
	}

	// Trigger preview deploy/teardown for preview-enabled apps.
	previewTriggered := s.handlePreviewEvent(repo, branch, commit, eventType, isDelete, prAction)

	if len(matchedNames) == 0 {
		if iacPush {
			writeJSON(w, http.StatusOK, map[string]string{"message": "accepted: IaC repo push"})
			return
		}
		if previewTriggered {
			writeJSON(w, http.StatusOK, map[string]string{"message": "accepted: preview"})
			return
		}
		slog.Info("webhook",
			"event", eventType,
			"repo", repo,
			"branch", branch,
			"result", "ignored: no matching apps",
		)
		writeJSON(w, http.StatusOK, map[string]string{"message": "no matching apps"})
		return
	}

	slices.Sort(matchedNames)

	slog.Info("webhook",
		"event", eventType,
		"repo", repo,
		"branch", branch,
		"result", fmt.Sprintf("accepted: %s", strings.Join(matchedNames, ",")),
	)

	for _, name := range matchedNames {
		app := s.Config.Apps[name]
		req := DeployRequest{
			AppName:  name,
			App:      app,
			Repo:     repo,
			Branch:   branch,
			Commit:   commit,
			CloneURL: cloneURL,
		}
		go s.OnDeploy(req)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "accepted",
		"apps":    matchedNames,
	})
}

// handlePreviewEvent dispatches preview deploy or teardown for preview-enabled apps.
// Returns true if any preview action was triggered.
func (s *Server) handlePreviewEvent(repo, branch, commit, eventType string, isDelete bool, prAction string) bool {
	if s.OnPreviewDeploy == nil && s.OnPreviewTeardown == nil {
		return false
	}
	triggered := false
	for name, app := range s.Config.Apps {
		if app.Preview == nil || !app.Preview.Enabled || app.Repo != repo {
			continue
		}
		switch eventType {
		case "push":
			if branch == app.Branch {
				// Default branch push → production deploy, not a preview.
				continue
			}
			if isDelete {
				if s.OnPreviewTeardown != nil {
					triggered = true
					go s.OnPreviewTeardown(name, branch)
				}
			} else {
				if s.OnPreviewDeploy != nil {
					triggered = true
					go s.OnPreviewDeploy(name, branch, commit)
				}
			}
		case "pull_request":
			switch prAction {
			case "opened", "synchronize":
				if s.OnPreviewDeploy != nil {
					triggered = true
					go s.OnPreviewDeploy(name, branch, commit)
				}
			case "closed":
				if s.OnPreviewTeardown != nil {
					triggered = true
					go s.OnPreviewTeardown(name, branch)
				}
			}
		}
	}
	return triggered
}

func (s *Server) verifySignature(body []byte, sigHeader string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	sigBytes, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
