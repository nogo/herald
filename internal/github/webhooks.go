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

func errResult(repo string, err error) SyncResult {
	return SyncResult{Repo: repo, Action: "error", Error: err}
}

// WebhookStatus is returned by ListWebhookStatuses.
// Error is set when the status could not be determined (invalid repo,
// API failure, missing token scope, etc.) — distinct from "no webhook
// is registered," which is Found=false with Error=nil.
type WebhookStatus struct {
	Repo   string
	URL    string
	Active bool
	Found  bool
	Error  error
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

// doRequestJSON sends a request, reads and closes the body, and returns a
// GitHubError when the status code does not match wantStatus. The response
// headers are returned so callers can inspect things like Link for pagination.
func (c *GitHubClient) doRequestJSON(ctx context.Context, method, url string, body any, wantStatus int) ([]byte, http.Header, error) {
	resp, err := c.doRequest(ctx, method, url, body)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode != wantStatus {
		return nil, nil, &GitHubError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	return data, resp.Header, nil
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
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/hooks?per_page=100&page=%d", c.BaseURL, owner, repo, page)
		body, headers, err := c.doRequestJSON(ctx, http.MethodGet, url, nil, http.StatusOK)
		if err != nil {
			return nil, err
		}

		var hooks []Webhook
		if err := json.Unmarshal(body, &hooks); err != nil {
			return nil, fmt.Errorf("decoding webhooks response: %w", err)
		}
		all = append(all, hooks...)

		if !strings.Contains(headers.Get("Link"), `rel="next"`) {
			return all, nil
		}
	}
}

// CreateWebhook creates a new webhook on the given repository.
func (c *GitHubClient) CreateWebhook(ctx context.Context, owner, repo string, req CreateWebhookRequest) (*Webhook, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks", c.BaseURL, owner, repo)

	payload := struct {
		Name   string   `json:"name"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config WHConfig `json:"config"`
	}{
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

	body, _, err := c.doRequestJSON(ctx, http.MethodPost, url, payload, http.StatusCreated)
	if err != nil {
		return nil, err
	}

	var hook Webhook
	if err := json.Unmarshal(body, &hook); err != nil {
		return nil, fmt.Errorf("decoding create webhook response: %w", err)
	}
	return &hook, nil
}

// DeleteWebhook deletes a webhook by ID.
func (c *GitHubClient) DeleteWebhook(ctx context.Context, owner, repo string, id int64) error {
	url := fmt.Sprintf("%s/repos/%s/%s/hooks/%d", c.BaseURL, owner, repo, id)
	_, _, err := c.doRequestJSON(ctx, http.MethodDelete, url, nil, http.StatusNoContent)
	return err
}

// uniqueRepos returns a sorted, deduplicated list of repos referenced by repo
// stacks. If iacRepo is non-empty it is included in the set so the server's
// own IaC repo gets a webhook reconciled alongside stack repos.
func uniqueRepos(cfg *config.Config, iacRepo string) []string {
	repoSet := make(map[string]struct{})
	for _, stack := range cfg.Stacks {
		if stack.Repo != "" {
			repoSet[stack.Repo] = struct{}{}
		}
	}
	if iacRepo != "" {
		repoSet[iacRepo] = struct{}{}
	}
	return slices.Sorted(maps.Keys(repoSet))
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
// Subscribes to pull_request if any stack referencing the repo has preview enabled.
func eventsForRepo(cfg *config.Config, repo string) []string {
	for _, stack := range cfg.Stacks {
		if strings.EqualFold(stack.Repo, repo) && stack.Preview != nil && stack.Preview.Enabled {
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
// When force is true, existing webhooks are deleted and recreated (use after changing the webhook secret).
// iacRepo, if non-empty, is included in the reconciliation set so the server's own IaC repo
// is kept in sync alongside stack repos.
func SyncWebhooks(ctx context.Context, cfg *config.Config, store *secrets.Store, client *GitHubClient, force bool, iacRepo string) ([]SyncResult, error) {
	webhookSecret, err := store.Get("herald/webhook_secret")
	if err != nil {
		return nil, fmt.Errorf("getting webhook secret: %w", err)
	}

	targetURL := webhookURL(cfg)
	repos := uniqueRepos(cfg, iacRepo)

	results := make([]SyncResult, 0, len(repos))
	for _, repoFull := range repos {
		results = append(results, syncRepo(ctx, cfg, client, repoFull, targetURL, webhookSecret, force))
	}

	return results, nil
}

// ReconcileWebhooks syncs webhooks for every repo in config (create or repair)
// and prunes Herald hooks on repos that are no longer desired. `known` maps repo
// full name → hook ID from the previous reconcile (read from webhooks.json); any
// entry not in the current config — e.g. a stack removed or its `repo:` changed —
// has its hook deleted. Returns per-repo results (including "pruned") and the new
// repo → hook ID map for the caller to persist.
func ReconcileWebhooks(ctx context.Context, cfg *config.Config, store *secrets.Store, client *GitHubClient, force bool, iacRepo string, known map[string]int64) ([]SyncResult, map[string]int64, error) {
	webhookSecret, err := store.Get("herald/webhook_secret")
	if err != nil {
		return nil, nil, fmt.Errorf("getting webhook secret: %w", err)
	}

	targetURL := webhookURL(cfg)
	desired := uniqueRepos(cfg, iacRepo)
	desiredSet := make(map[string]bool, len(desired))

	results := make([]SyncResult, 0, len(desired))
	current := make(map[string]int64, len(desired))
	for _, repoFull := range desired {
		desiredSet[repoFull] = true
		r := syncRepo(ctx, cfg, client, repoFull, targetURL, webhookSecret, force)
		results = append(results, r)
		if r.ID != 0 {
			current[repoFull] = r.ID
		}
	}

	// Prune Herald hooks on repos no longer in config.
	for repoFull, id := range known {
		if desiredSet[repoFull] {
			continue
		}
		owner, repoName, splitErr := splitRepo(repoFull)
		if splitErr != nil {
			results = append(results, errResult(repoFull, splitErr))
			continue
		}
		if err := client.DeleteWebhook(ctx, owner, repoName, id); err != nil {
			results = append(results, errResult(repoFull, formatAPIError(repoFull, err)))
			continue
		}
		results = append(results, SyncResult{Repo: repoFull, Action: "pruned", ID: id})
	}

	return results, current, nil
}

func syncRepo(ctx context.Context, cfg *config.Config, client *GitHubClient, repoFull, targetURL, webhookSecret string, force bool) SyncResult {
	owner, repoName, err := splitRepo(repoFull)
	if err != nil {
		return errResult(repoFull, err)
	}

	hooks, err := client.ListWebhooks(ctx, owner, repoName)
	if err != nil {
		return errResult(repoFull, formatAPIError(repoFull, err))
	}

	var heraldHook *Webhook
	for i := range hooks {
		if hooks[i].Config.URL == targetURL {
			heraldHook = &hooks[i]
			break
		}
	}

	if heraldHook != nil && heraldHook.Active && !force {
		return SyncResult{Repo: repoFull, Action: "exists", ID: heraldHook.ID}
	}

	// Delete existing hook before recreating (inactive, or force mode).
	if heraldHook != nil {
		if err := client.DeleteWebhook(ctx, owner, repoName, heraldHook.ID); err != nil {
			return errResult(repoFull, formatAPIError(repoFull, err))
		}
	}

	hook, err := client.CreateWebhook(ctx, owner, repoName, CreateWebhookRequest{
		URL:    targetURL,
		Secret: webhookSecret,
		Events: eventsForRepo(cfg, repoFull),
	})
	if err != nil {
		return errResult(repoFull, formatAPIError(repoFull, err))
	}

	return SyncResult{Repo: repoFull, Action: "created", ID: hook.ID}
}

// ListWebhookStatuses returns the webhook status for each unique repo in config.
// iacRepo, if non-empty, is included so the server's own IaC repo appears in the listing.
func ListWebhookStatuses(ctx context.Context, cfg *config.Config, client *GitHubClient, iacRepo string) []WebhookStatus {
	targetURL := webhookURL(cfg)
	repos := uniqueRepos(cfg, iacRepo)

	statuses := make([]WebhookStatus, 0, len(repos))
	for _, repoFull := range repos {
		status := WebhookStatus{Repo: repoFull}

		owner, repoName, err := splitRepo(repoFull)
		if err != nil {
			status.Error = err
			statuses = append(statuses, status)
			continue
		}

		hooks, err := client.ListWebhooks(ctx, owner, repoName)
		if err != nil {
			status.Error = formatAPIError(repoFull, err)
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
