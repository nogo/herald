package webhook_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/webhook"
)

const testSecret = "test-secret"

func newServer() *webhook.Server {
	return &webhook.Server{
		Config: &config.Config{
			Server: config.Server{
				Name:         "test",
				DeployDomain: "deploy.example.com",
				ServicesDir:  "/opt/stacks",
			},
			Stacks: map[string]config.Stack{
				"budget":  {Repo: "nogo/budget-app", Branch: "main"},
				"tracker": {Repo: "nogo/budget-app", Branch: "main"},
				"other":   {Repo: "nogo/other-app", Branch: "main"},
			},
		},
		Secret:   testSecret,
		OnDeploy: func(webhook.DeployRequest) {},
	}
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(handler http.Handler, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthCheck(t *testing.T) {
	srv := newServer()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %q", body["status"])
	}
}

func TestMissingSignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app"},"pusher":{"name":"test"}}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event": "push",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestWrongSignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app"},"pusher":{"name":"test"}}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": "sha256=" + strings.Repeat("a", 64),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["error"] != "invalid signature" {
		t.Errorf("unexpected error body: %v", resp)
	}
}

func TestValidPushMatched(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app","clone_url":"https://github.com/nogo/budget-app.git"},"pusher":{"name":"test"}}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["message"] != "accepted" {
		t.Errorf("expected message=accepted, got %v", resp["message"])
	}
	stacks, ok := resp["stacks"].([]any)
	if !ok || len(stacks) != 2 {
		t.Errorf("expected 2 stacks, got %v", resp["stacks"])
	}
}

func TestValidPushNoMatch(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/dev","after":"abc123","repository":{"full_name":"nogo/budget-app","clone_url":"https://github.com/nogo/budget-app.git"},"pusher":{"name":"test"}}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["message"] != "no matching stacks" {
		t.Errorf("expected no matching stacks, got %q", resp["message"])
	}
}

func TestPingEvent(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "ping",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["message"] != "pong" {
		t.Errorf("expected pong, got %q", resp["message"])
	}
}

func TestUnknownEvent(t *testing.T) {
	body := []byte(`{}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "star",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["message"] != "event ignored" {
		t.Errorf("expected event ignored, got %q", resp["message"])
	}
}

func TestMalformedJSON(t *testing.T) {
	body := []byte(`not json`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["error"] != "invalid payload" {
		t.Errorf("expected invalid payload, got %q", resp["error"])
	}
}

func TestPullRequestEvent(t *testing.T) {
	body := []byte(`{"action":"opened","number":1,"pull_request":{"head":{"ref":"feature","sha":"def456"},"base":{"ref":"main","sha":"abc123"},"merged":false},"repository":{"full_name":"nogo/budget-app","clone_url":"https://github.com/nogo/budget-app.git"}}`)
	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "pull_request",
		"X-Hub-Signature-256": sign(body),
	})
	// feature branch doesn't match any app (apps are on main), so no matching apps
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHMACConstantTimeComparison(t *testing.T) {
	// Verify that a signature computed with a different secret is rejected.
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app"},"pusher":{"name":"test"}}`)
	wrongMac := hmac.New(sha256.New, []byte("wrong-secret"))
	wrongMac.Write(body)
	wrongSig := "sha256=" + hex.EncodeToString(wrongMac.Sum(nil))

	rec := post(newServer().Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": wrongSig,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for wrong-secret HMAC, got %d", rec.Code)
	}
}

func TestDeployCallbackFired(t *testing.T) {
	called := make(chan webhook.DeployRequest, 4)
	srv := newServer()
	srv.OnDeploy = func(req webhook.DeployRequest) {
		called <- req
	}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app","clone_url":"https://github.com/nogo/budget-app.git"},"pusher":{"name":"test"}}`)
	rec := post(srv.Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Collect the two deploy calls (budget + tracker).
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		req := <-called
		seen[req.StackName] = true
	}
	if !seen["budget"] || !seen["tracker"] {
		t.Errorf("expected deploy for budget and tracker, got %v", seen)
	}
}

func TestPathStackNotMatchedByWebhook(t *testing.T) {
	called := make(chan webhook.DeployRequest, 4)
	srv := &webhook.Server{
		Config: &config.Config{
			Stacks: map[string]config.Stack{
				// path stack: no Repo field
				"myservice": {Path: "/opt/services/myservice", Branch: ""},
				// repo stack on a different repo
				"other": {Repo: "nogo/other-app", Branch: "main"},
			},
		},
		Secret: testSecret,
		OnDeploy: func(req webhook.DeployRequest) {
			called <- req
		},
	}

	// Push to a repo that matches nothing (path stacks have no Repo).
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nogo/budget-app","clone_url":"https://github.com/nogo/budget-app.git"},"pusher":{"name":"test"}}`)
	rec := post(srv.Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// No deploy should fire for path stacks.
	select {
	case req := <-called:
		t.Errorf("unexpected deploy for stack %q (path stacks must not be matched by webhook)", req.StackName)
	default:
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["message"] != "no matching stacks" {
		t.Errorf("expected no matching stacks, got %q", resp["message"])
	}
}

func TestTagPush(t *testing.T) {
	called := make(chan webhook.DeployRequest, 4)
	srv := &webhook.Server{
		Config: &config.Config{
			Stacks: map[string]config.Stack{
				"myapp": {Repo: "nogo/myapp", Branch: "main", TagPattern: "v*"},
			},
		},
		Secret: testSecret,
		OnDeploy: func(req webhook.DeployRequest) {
			called <- req
		},
	}

	body := []byte(`{"ref":"refs/tags/v1.2.3","after":"abc123","deleted":false,"repository":{"full_name":"nogo/myapp","clone_url":"https://github.com/nogo/myapp.git"},"pusher":{"name":"test"}}`)
	rec := post(srv.Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case req := <-called:
		if req.StackName != "myapp" {
			t.Errorf("StackName = %q, want %q", req.StackName, "myapp")
		}
		if req.Tag != "v1.2.3" {
			t.Errorf("Tag = %q, want %q", req.Tag, "v1.2.3")
		}
		if req.Ref != "refs/tags/v1.2.3" {
			t.Errorf("Ref = %q, want %q", req.Ref, "refs/tags/v1.2.3")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for deploy callback")
	}
}

func TestIaCRepoPush(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := &webhook.Server{
		Config: &config.Config{
			Stacks: map[string]config.Stack{},
		},
		Secret:    testSecret,
		OnDeploy:  func(webhook.DeployRequest) {},
		IaCRepo:   "nogo/srv2",
		OnIaCPush: func() { called <- struct{}{} },
	}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","deleted":false,"repository":{"full_name":"nogo/srv2","clone_url":"https://github.com/nogo/srv2.git"},"pusher":{"name":"test"}}`)
	rec := post(srv.Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-called:
		// expected
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for OnIaCPush callback")
	}
}

func TestPreviewDeploy(t *testing.T) {
	type previewEvent struct{ name, branch, commit string }
	called := make(chan previewEvent, 1)
	srv := &webhook.Server{
		Config: &config.Config{
			Stacks: map[string]config.Stack{
				"myapp": {
					Repo:    "nogo/myapp",
					Branch:  "main",
					Preview: &config.PreviewConfig{Enabled: true},
				},
			},
		},
		Secret:   testSecret,
		OnDeploy: func(webhook.DeployRequest) {},
		OnPreviewDeploy: func(name, branch, commit string) {
			called <- previewEvent{name, branch, commit}
		},
	}

	body := []byte(`{"ref":"refs/heads/feature-xyz","after":"def456","deleted":false,"repository":{"full_name":"nogo/myapp","clone_url":"https://github.com/nogo/myapp.git"},"pusher":{"name":"test"}}`)
	rec := post(srv.Handler(), "/webhook", body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": sign(body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case ev := <-called:
		if ev.name != "myapp" {
			t.Errorf("name = %q, want %q", ev.name, "myapp")
		}
		if ev.branch != "feature-xyz" {
			t.Errorf("branch = %q, want %q", ev.branch, "feature-xyz")
		}
		if ev.commit != "def456" {
			t.Errorf("commit = %q, want %q", ev.commit, "def456")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for OnPreviewDeploy callback")
	}
}
