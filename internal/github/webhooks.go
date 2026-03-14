package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
)

const defaultBaseURL = "https://api.github.com"
const userAgent = "herald/dev"

// GitHubClient is a minimal GitHub REST API client.
type GitHubClient struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
	Logger  *slog.Logger
}

// NewGitHubClient creates a GitHubClient with the given token.
func NewGitHubClient(token string, logger *slog.Logger) *GitHubClient {
	return &GitHubClient{
		Token:   token,
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Logger:  logger,
	}
}

// GitHubError represents an error response from the GitHub API.
type GitHubError struct {
	StatusCode int
	Body       string
}

func (e *GitHubError) Error() string {
	return fmt.Sprintf("github API: HTTP %d: %s", e.StatusCode, e.Body)
}

// Webhook represents a GitHub repository webhook.
type Webhook struct {
	ID     int64    `json:"id"`
	Active bool     `json:"active"`
	Config WHConfig `json:"config"`
	Events []string `json:"events"`
	URL    string   // extracted from Config.URL
}

// WHConfig is the webhook configuration object from the GitHub API.
type WHConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret,omitempty"` // only set on create, never returned by API
	InsecureSSL string `json:"insecure_ssl"`
}

// CreateWebhookRequest holds the parameters for creating a webhook.
type CreateWebhookRequest struct {
	URL    string
	Secret string
	Events []string
}

// SyncResult holds the outcome of syncing a single repo.
type SyncResult struct {
	Repo   string
	Action string // "exists", "created", "error"
	ID     int64
	Error  error
}

// WebhookStatus is returned by ListWebhookStatuses.
type WebhookStatus struct {
	Repo   string
	URL    string
	Active bool
	Found  bool
}

func (c *GitHubClient) doRequest(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	// Check for rate limiting before returning.
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		resetTime := parseUnixTimestamp(resp.Header.Get("X-RateLimit-Reset"))
		c.Logger.Warn("GitHub API rate limit exceeded", "reset_at", resetTime)
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub API rate limit exceeded, resets at %s", resetTime)
	}

	return resp, nil
}

func parseUnixTimestamp(s string) string {
	ts, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return s
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// ListWebhooks returns all webhooks for a repository, following pagination.
func (c *GitHubClient) ListWebhooks(ctx context.Context, owner, repo string) ([]Webhook, error) {
	var all []Webhook
	page := 1

	for {
		url := fmt.Sprintf("%s/repos/%s/%s/hooks?per_page=100&page=%d", c.BaseURL, owner, repo, page)
		resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, &GitHubError{StatusCode: resp.StatusCode, Body: string(body)}
		}

		var hooks []Webhook
		if err := json.Unmarshal(body, &hooks); err != nil {
			return nil, fmt.Errorf("decoding webhooks response: %w", err)
		}

		for i := range hooks {
			hooks[i].URL = hooks[i].Config.URL
		}

		all = append(all, hooks...)

		if !strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
			break
		}
		page++
	}

	return all, nil
}

// CreateWebhook creates a new webhook on the given repository.
func (c *GitHubClient) CreateWebhook(ctx context.Context, owner, repo string, req CreateWebhookRequest) (*Webhook, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks", c.BaseURL, owner, repo)

	type createBody struct {
		Name   string   `json:"name"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config WHConfig `json:"config"`
	}

	payload := createBody{
		Name:   "web",
		Active: true,
		Events: req.Events,
		Config: WHConfig{
			URL:         req.URL,
			ContentType: "json",
			Secret:      req.Secret,
			InsecureSSL: "0",
		},
	}

	resp, err := c.doRequest(ctx, http.MethodPost, url, payload)
	if err != nil {
		return nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, &GitHubError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var hook Webhook
	if err := json.Unmarshal(respBody, &hook); err != nil {
		return nil, fmt.Errorf("decoding create webhook response: %w", err)
	}
	hook.URL = hook.Config.URL

	return &hook, nil
}

// DeleteWebhook deletes a webhook by ID.
func (c *GitHubClient) DeleteWebhook(ctx context.Context, owner, repo string, id int64) error {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks/%d", c.BaseURL, owner, repo, id)

	resp, err := c.doRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusNoContent {
		return &GitHubError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	return nil
}

// uniqueRepos returns a sorted, deduplicated list of repos referenced by config.Apps.
func uniqueRepos(cfg *config.Config) []string {
	repoSet := make(map[string]struct{})
	for _, app := range cfg.Apps {
		repoSet[app.Repo] = struct{}{}
	}
	return slices.Collect(maps.Keys(repoSet))
}

// splitRepo splits "owner/repo" into its two components.
func splitRepo(fullRepo string) (string, string, error) {
	parts := strings.SplitN(fullRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q, expected owner/repo", fullRepo)
	}
	return parts[0], parts[1], nil
}

// webhookURL returns the herald webhook URL derived from config.
func webhookURL(cfg *config.Config) string {
	return "https://" + cfg.Server.DeployDomain + "/webhook"
}

// eventsForRepo returns the events to subscribe to for a repo.
// Subscribes to pull_request if any app referencing the repo has preview enabled.
func eventsForRepo(cfg *config.Config, repo string) []string {
	for _, app := range cfg.Apps {
		if app.Repo == repo && app.Preview != nil && app.Preview.Enabled {
			return []string{"push", "pull_request"}
		}
	}
	return []string{"push"}
}

// formatAPIError wraps a GitHub API error with actionable messaging.
func formatAPIError(repo string, err error) error {
	var ghErr *GitHubError
	if errors.As(err, &ghErr) && ghErr.StatusCode == http.StatusNotFound {
		return fmt.Errorf("cannot manage webhooks for %q: ensure the token has 'admin:repo_hook' scope and access to this repository", repo)
	}
	return err
}

// SyncWebhooks ensures herald webhooks are registered on all repos referenced in config.
func SyncWebhooks(ctx context.Context, cfg *config.Config, store *secrets.Store, client *GitHubClient) ([]SyncResult, error) {
	webhookSecret, err := store.Get("herald/webhook_secret")
	if err != nil {
		return nil, fmt.Errorf("getting webhook secret: %w", err)
	}

	targetURL := webhookURL(cfg)
	repos := uniqueRepos(cfg)
	slices.Sort(repos)

	results := make([]SyncResult, 0, len(repos))
	for _, repoFull := range repos {
		results = append(results, syncRepo(ctx, cfg, client, repoFull, targetURL, webhookSecret))
	}

	return results, nil
}

func syncRepo(ctx context.Context, cfg *config.Config, client *GitHubClient, repoFull, targetURL, webhookSecret string) SyncResult {
	result := SyncResult{Repo: repoFull}

	owner, repoName, err := splitRepo(repoFull)
	if err != nil {
		result.Action = "error"
		result.Error = err
		return result
	}

	hooks, err := client.ListWebhooks(ctx, owner, repoName)
	if err != nil {
		result.Action = "error"
		result.Error = formatAPIError(repoFull, err)
		return result
	}

	var heraldHook *Webhook
	for i := range hooks {
		if hooks[i].Config.URL == targetURL {
			heraldHook = &hooks[i]
			break
		}
	}

	if heraldHook != nil && heraldHook.Active {
		result.Action = "exists"
		result.ID = heraldHook.ID
		return result
	}

	// Delete inactive hook before recreating.
	if heraldHook != nil {
		if err := client.DeleteWebhook(ctx, owner, repoName, heraldHook.ID); err != nil {
			result.Action = "error"
			result.Error = formatAPIError(repoFull, err)
			return result
		}
	}

	hook, err := client.CreateWebhook(ctx, owner, repoName, CreateWebhookRequest{
		URL:    targetURL,
		Secret: webhookSecret,
		Events: eventsForRepo(cfg, repoFull),
	})
	if err != nil {
		result.Action = "error"
		result.Error = formatAPIError(repoFull, err)
		return result
	}

	result.Action = "created"
	result.ID = hook.ID
	return result
}

// ListWebhookStatuses returns the webhook status for each unique repo in config.
func ListWebhookStatuses(ctx context.Context, cfg *config.Config, client *GitHubClient) []WebhookStatus {
	targetURL := webhookURL(cfg)
	repos := uniqueRepos(cfg)
	slices.Sort(repos)

	statuses := make([]WebhookStatus, 0, len(repos))
	for _, repoFull := range repos {
		status := WebhookStatus{Repo: repoFull}

		owner, repoName, err := splitRepo(repoFull)
		if err != nil {
			statuses = append(statuses, status)
			continue
		}

		hooks, err := client.ListWebhooks(ctx, owner, repoName)
		if err != nil {
			statuses = append(statuses, status)
			continue
		}

		for _, hook := range hooks {
			if hook.Config.URL == targetURL {
				status.Found = true
				status.URL = hook.Config.URL
				status.Active = hook.Active
				break
			}
		}

		statuses = append(statuses, status)
	}

	return statuses
}
