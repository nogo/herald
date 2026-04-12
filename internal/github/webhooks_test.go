package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nogo/herald/internal/config"
)

func newTestClient(t *testing.T, handler http.Handler) *GitHubClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewGitHubClient("test-token", slog.Default())
	client.BaseURL = srv.URL
	return client
}

func TestListWebhooks(t *testing.T) {
	hooks := []Webhook{
		{ID: 1, Active: true, Config: WHConfig{URL: "https://example.com/webhook", ContentType: "json"}, Events: []string{"push"}},
		{ID: 2, Active: false, Config: WHConfig{URL: "https://other.com/hook", ContentType: "json"}, Events: []string{"push"}},
	}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			http.Error(w, "bad accept", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hooks)
	}))

	got, err := client.ListWebhooks(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d webhooks, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].URL != "https://example.com/webhook" {
		t.Errorf("got[0] = %+v, unexpected", got[0])
	}
}

func TestListWebhooks_NotFound(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))

	_, err := client.ListWebhooks(context.Background(), "owner", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ghErr *GitHubError
	if !isGitHubError(err, &ghErr) || ghErr.StatusCode != http.StatusNotFound {
		t.Errorf("want 404 GitHubError, got %v", err)
	}
}

func TestListWebhooks_RateLimit(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusForbidden)
	}))

	_, err := client.ListWebhooks(context.Background(), "owner", "repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestListWebhooks_Pagination(t *testing.T) {
	page1 := []Webhook{{ID: 1, Active: true, Config: WHConfig{URL: "https://a.com/hook"}}}
	page2 := []Webhook{{ID: 2, Active: true, Config: WHConfig{URL: "https://b.com/hook"}}}

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(page2)
			return
		}
		w.Header().Set("Link", `<http://example.com?page=2>; rel="next"`)
		json.NewEncoder(w).Encode(page1)
	}))

	got, err := client.ListWebhooks(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d webhooks, want 2", len(got))
	}
}

func TestCreateWebhook(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "web" {
			http.Error(w, "bad name", http.StatusBadRequest)
			return
		}
		// Verify secret is NOT logged (just ensure it reaches the server)
		cfg, _ := body["config"].(map[string]any)
		if cfg["secret"] == "" {
			http.Error(w, "missing secret", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Webhook{
			ID:     42,
			Active: true,
			Config: WHConfig{URL: "https://deploy.example.com/webhook", ContentType: "json"},
			Events: []string{"push"},
		})
	}))

	hook, err := client.CreateWebhook(context.Background(), "owner", "repo", CreateWebhookRequest{
		URL:    "https://deploy.example.com/webhook",
		Secret: "supersecret",
		Events: []string{"push"},
	})
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if hook.ID != 42 {
		t.Errorf("got ID %d, want 42", hook.ID)
	}
	if hook.URL != "https://deploy.example.com/webhook" {
		t.Errorf("got URL %q, want deploy URL", hook.URL)
	}
}

func TestCreateWebhook_NotFound(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))

	_, err := client.CreateWebhook(context.Background(), "owner", "repo", CreateWebhookRequest{
		URL:    "https://deploy.example.com/webhook",
		Secret: "s",
		Events: []string{"push"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ghErr *GitHubError
	if !isGitHubError(err, &ghErr) || ghErr.StatusCode != http.StatusNotFound {
		t.Errorf("want 404 GitHubError, got %v", err)
	}
}

func TestDeleteWebhook(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := client.DeleteWebhook(context.Background(), "owner", "repo", 99); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
}

func TestDeleteWebhook_Error(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))

	err := client.DeleteWebhook(context.Background(), "owner", "repo", 99)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUniqueRepos(t *testing.T) {
	cfg := &config.Config{
		Apps: map[string]config.App{
			"budget":   {Repo: "nogo/budget-app"},
			"tracker":  {Repo: "nogo/budget-app"}, // duplicate
			"sidenote": {Repo: "nogo/sidenote"},
		},
	}

	repos := uniqueRepos(cfg)
	if len(repos) != 2 {
		t.Errorf("got %d repos, want 2: %v", len(repos), repos)
	}
	// Verify both unique repos are present.
	found := make(map[string]bool)
	for _, r := range repos {
		found[r] = true
	}
	if !found["nogo/budget-app"] || !found["nogo/sidenote"] {
		t.Errorf("missing expected repos, got %v", repos)
	}
}

func TestEventsForRepo(t *testing.T) {
	cfg := &config.Config{
		Apps: map[string]config.App{
			"no-preview": {Repo: "nogo/plain"},
			"with-preview": {
				Repo:    "nogo/preview-app",
				Preview: &config.PreviewConfig{Enabled: true, Domain: "*.preview.example.com"},
			},
		},
	}

	got := eventsForRepo(cfg, "nogo/plain")
	if len(got) != 1 || got[0] != "push" {
		t.Errorf("plain repo: got %v, want [push]", got)
	}

	got = eventsForRepo(cfg, "nogo/preview-app")
	if len(got) != 2 {
		t.Errorf("preview repo: got %v, want [push pull_request]", got)
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input   string
		owner   string
		repo    string
		wantErr bool
	}{
		{"nogo/herald", "nogo", "herald", false},
		{"org/repo-name", "org", "repo-name", false},
		{"invalid", "", "", true},
		{"", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, err := splitRepo(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitRepo(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && (owner != tt.owner || repo != tt.repo) {
				t.Errorf("got (%q, %q), want (%q, %q)", owner, repo, tt.owner, tt.repo)
			}
		})
	}
}

func TestFormatAPIError(t *testing.T) {
	notFoundErr := &GitHubError{StatusCode: http.StatusNotFound, Body: "Not Found"}
	wrapped := formatAPIError("nogo/repo", notFoundErr)
	if wrapped == nil {
		t.Fatal("expected error, got nil")
	}
	if fmt.Sprintf("%v", wrapped) == fmt.Sprintf("%v", notFoundErr) {
		t.Error("expected actionable error message, got raw GitHub error")
	}

	otherErr := &GitHubError{StatusCode: http.StatusInternalServerError, Body: "server error"}
	wrapped2 := formatAPIError("nogo/repo", otherErr)
	if wrapped2 != otherErr {
		t.Error("non-404 errors should pass through unchanged")
	}
}

// isGitHubError is a helper to unwrap *GitHubError from an error chain.
func isGitHubError(err error, target **GitHubError) bool {
	var ghErr *GitHubError
	if ok := (func() bool {
		for {
			if e, ok2 := err.(*GitHubError); ok2 {
				ghErr = e
				return true
			}
			type unwrapper interface{ Unwrap() error }
			u, ok2 := err.(unwrapper)
			if !ok2 {
				return false
			}
			err = u.Unwrap()
		}
	})(); ok {
		*target = ghErr
		return true
	}
	return false
}
