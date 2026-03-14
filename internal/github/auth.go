package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default OAuth App Client ID for Herald.
// Users can override this by registering their own GitHub OAuth App
// and setting the HERALD_GITHUB_CLIENT_ID env var.
const DefaultClientID = ""

// DeviceCodeResponse is returned by the device authorization endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse is returned by the access token endpoint.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// DeviceFlowAuth performs GitHub OAuth Device Flow authentication.
// It prints instructions to w, then polls until the user authorizes.
//
// The clientID must be from a registered GitHub OAuth App.
// Required scopes: repo, admin:repo_hook.
func DeviceFlowAuth(ctx context.Context, w io.Writer, clientID string) (string, error) {
	if clientID == "" {
		return "", fmt.Errorf("GitHub OAuth Client ID is required.\n" +
			"Register a GitHub OAuth App at: https://github.com/settings/applications/new\n" +
			"  Application name: Herald\n" +
			"  Homepage URL: https://github.com/nogo/herald\n" +
			"  Authorization callback URL: http://localhost (not used)\n" +
			"  Enable Device Flow: checked\n" +
			"Then set HERALD_GITHUB_CLIENT_ID or pass --client-id")
	}

	// Step 1: Request device code
	dc, err := requestDeviceCode(ctx, clientID)
	if err != nil {
		return "", fmt.Errorf("requesting device code: %w", err)
	}

	// Step 2: Display instructions
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  GitHub Authentication")
	fmt.Fprintf(w, "  Open this URL in your browser:  %s\n", dc.VerificationURI)
	fmt.Fprintf(w, "  Enter code:                     %s\n", dc.UserCode)
	fmt.Fprintln(w)
	fmt.Fprint(w, "  Waiting for authorization...")

	// Step 3: Poll for token
	interval := time.Duration(dc.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(w, " cancelled")
			return "", ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			fmt.Fprintln(w, " expired")
			return "", fmt.Errorf("device code expired, please try again")
		}

		token, err := pollAccessToken(ctx, clientID, dc.DeviceCode)
		if err != nil {
			return "", err
		}
		if token != "" {
			fmt.Fprintln(w, " authorized")

			// Verify who we authenticated as
			user, err := GetUser(ctx, token)
			if err == nil && user != "" {
				fmt.Fprintf(w, "  Authenticated as: %s\n", user)
			}

			return token, nil
		}
	}
}

func requestDeviceCode(ctx context.Context, clientID string) (*DeviceCodeResponse, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"repo admin:repo_hook"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/device/code",
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: HTTP %d: %s", resp.StatusCode, body)
	}

	var dc DeviceCodeResponse
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}

	if dc.DeviceCode == "" {
		return nil, fmt.Errorf("empty device code in response: %s", body)
	}

	return &dc, nil
}

func pollAccessToken(ctx context.Context, clientID, deviceCode string) (string, error) {
	data := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/oauth/access_token",
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}

	switch tr.Error {
	case "":
		return tr.AccessToken, nil
	case "authorization_pending":
		return "", nil // keep polling
	case "slow_down":
		return "", nil // keep polling (caller will wait interval)
	case "expired_token":
		return "", fmt.Errorf("device code expired, please try again")
	case "access_denied":
		return "", fmt.Errorf("authorization denied by user")
	default:
		return "", fmt.Errorf("OAuth error: %s: %s", tr.Error, tr.ErrorDesc)
	}
}

// GetUser returns the authenticated user's login for the given token.
func GetUser(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Login, nil
}
