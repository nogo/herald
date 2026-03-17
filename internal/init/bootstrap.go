package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/secrets"
)

// Options holds the configuration for bootstrapping.
type Options struct {
	ServerRepo  string // GitHub repo path, e.g. "nogo/srv2"
	GitHubToken string
	DataDir     string
	StacksDir   string // Override stacks dir from config
	HeraldPort  int    // Port herald listens on (default 9483)
}

// CheckPrerequisites verifies all system prerequisites are met.
func CheckPrerequisites(ctx context.Context, w io.Writer, dataDir string) error {
	fmt.Fprintln(w, "Checking prerequisites...")

	// 1. Docker
	dockerVersion, err := checkDocker(ctx)
	if err != nil {
		fmt.Fprintf(w, "  Docker:          ✗ not found\n")
		return fmt.Errorf("Docker is not installed or not accessible. Install Docker and ensure the current user is in the 'docker' group.")
	}
	fmt.Fprintf(w, "  Docker:          ✓ %s\n", dockerVersion)

	// 2. Docker Compose
	composeVersion, err := checkDockerCompose(ctx)
	if err != nil {
		fmt.Fprintf(w, "  Docker Compose:  ✗ not found\n")
		return fmt.Errorf("Docker Compose plugin is not installed.")
	}
	fmt.Fprintf(w, "  Docker Compose:  ✓ %s\n", composeVersion)

	// 3. Git
	gitVersion, err := checkGit(ctx)
	if err != nil {
		fmt.Fprintf(w, "  Git:             ✗ not found\n")
		return fmt.Errorf("git is not installed.")
	}
	fmt.Fprintf(w, "  Git:             ✓ %s\n", gitVersion)

	// 4. Ports 80/443
	portsOK, err := checkPorts(ctx)
	if err != nil || !portsOK {
		fmt.Fprintf(w, "  Ports 80/443:    ✗ in use\n")
		return fmt.Errorf("Ports 80/443 are in use. Stop the existing proxy before running init.")
	}
	fmt.Fprintf(w, "  Ports 80/443:    ✓ available\n")

	// 5. Data dir
	if err := checkDataDir(dataDir); err != nil {
		fmt.Fprintf(w, "  Data directory:  ✗ %s\n", dataDir)
		return err
	}
	fmt.Fprintf(w, "  Data directory:  ✓ %s (writable)\n", dataDir)

	return nil
}

func checkDocker(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "Docker {{.ServerVersion}}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func checkDockerCompose(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "compose", "version", "--short").Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v, nil
}

func checkGit(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return "", err
	}
	// "git version 2.43.0" -> "2.43.0"
	parts := strings.Fields(string(out))
	if len(parts) >= 3 {
		return parts[2], nil
	}
	return strings.TrimSpace(string(out)), nil
}

// checkPorts returns true if ports 80 and 443 are free.
func checkPorts(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx, "ss", "-tlnp").Output()
	if err != nil {
		// ss not available — skip check, assume free
		return true, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, ":80 ") || strings.Contains(line, ":443 ") {
			return false, nil
		}
	}
	return true, nil
}

func checkDataDir(dataDir string) error {
	info, err := os.Stat(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		if mkErr := os.MkdirAll(dataDir, 0700); mkErr != nil {
			return fmt.Errorf("data directory %q does not exist and could not be created.\nRun: sudo mkdir -p %s && sudo chown $(whoami) %s",
				dataDir, dataDir, dataDir)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("data directory %q: %w", dataDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data directory %q is not a directory", dataDir)
	}
	testFile := filepath.Join(dataDir, ".write_test")
	f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("data directory %q is not writable.\nRun: sudo chown $(whoami) %s", dataDir, dataDir)
	}
	f.Close()
	os.Remove(testFile)
	return nil
}

// Bootstrap runs the full server initialisation sequence.
func Bootstrap(ctx context.Context, w io.Writer, opts Options) error {
	if opts.HeraldPort == 0 {
		opts.HeraldPort = 9483
	}

	// Step 1: Clone server IaC repo
	repoDir := filepath.Join(opts.DataDir, "repo")
	configPath := filepath.Join(repoDir, "config.yml")

	fmt.Fprintln(w, "\nCloning server repository...")
	if err := cloneOrPull(ctx, w, opts.ServerRepo, repoDir, opts.GitHubToken); err != nil {
		return fmt.Errorf("cloning server repo: %w", err)
	}

	// Step 2: Load and validate config
	fmt.Fprintln(w, "\nLoading configuration...")
	fmt.Fprintf(w, "  → %s\n", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if opts.StacksDir != "" {
		cfg.Server.StacksDir = opts.StacksDir
	}
	// Use port from config if not explicitly set via CLI.
	if cfg.Server.Port > 0 {
		opts.HeraldPort = cfg.Server.Port
	}
	fmt.Fprintf(w, "  ✓ Config loaded: %d app%s, %d service%s\n",
		len(cfg.Apps), plural(len(cfg.Apps)),
		len(cfg.Services), plural(len(cfg.Services)))

	// Step 3: Initialize secrets store
	fmt.Fprintln(w, "\nInitializing secrets store...")
	store := secrets.NewStore(opts.DataDir)
	ageKeyPath := filepath.Join(opts.DataDir, "age.key")
	_, statErr := os.Stat(ageKeyPath)
	keyExists := statErr == nil
	if err := store.Init(); err != nil {
		return fmt.Errorf("initializing secrets store: %w", err)
	}
	if !keyExists {
		fmt.Fprintf(w, "  → Generated age key: %s\n", ageKeyPath)
		fmt.Fprintln(w, "  ✓ Secrets store initialized")
		fmt.Fprintf(w, "  ⚠ IMPORTANT: Back up %s — it cannot be recovered if lost!\n", ageKeyPath)
	} else {
		fmt.Fprintf(w, "  → Age key already exists: %s\n", ageKeyPath)
		fmt.Fprintln(w, "  ✓ Secrets store ready")
	}

	// Step 4: Set webhook secret
	fmt.Fprintln(w, "\nConfiguring webhook secret...")
	if _, getErr := store.Get("herald/webhook_secret"); getErr != nil {
		secret, err := generateSecret()
		if err != nil {
			return fmt.Errorf("generating webhook secret: %w", err)
		}
		if err := store.Set("herald/webhook_secret", secret); err != nil {
			return fmt.Errorf("storing webhook secret: %w", err)
		}
		fmt.Fprintln(w, "  → Generated webhook secret")
		fmt.Fprintln(w, "  ✓ Webhook secret stored in secrets store")
	} else {
		fmt.Fprintln(w, "  → Webhook secret already exists, skipping")
		fmt.Fprintln(w, "  ✓ Webhook secret ready")
	}

	// Step 5: Store GitHub token
	if opts.GitHubToken != "" {
		fmt.Fprintln(w, "\nStoring GitHub token...")
		if err := store.Set("herald/github_token", opts.GitHubToken); err != nil {
			return fmt.Errorf("storing GitHub token: %w", err)
		}
		fmt.Fprintln(w, "  ✓ GitHub token stored in secrets store")
	}

	// Step 6: Create directories
	fmt.Fprintln(w, "\nCreating directories...")
	dirs := []string{
		filepath.Join(cfg.Server.StacksDir, "apps"),
		filepath.Join(cfg.Server.StacksDir, "stacks"),
		filepath.Join(cfg.Server.StacksDir, "envs"),
	}
	for _, d := range dirs {
		fmt.Fprintf(w, "  → %s/\n", d)
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	fmt.Fprintln(w, "  ✓ Directories created")

	// Step 7: Start Caddy
	fmt.Fprintln(w, "\nStarting Caddy reverse proxy...")
	caddyMgr := &caddy.CaddyManager{
		Config:     cfg,
		Logger:     slog.Default(),
		HeraldPort: opts.HeraldPort,
	}
	running, _ := caddyMgr.IsRunning(ctx)
	if running {
		fmt.Fprintln(w, "  → Caddy already running, skipping")
		fmt.Fprintln(w, "  ✓ Caddy running on :80/:443")
	} else {
		fmt.Fprintln(w, "  → Creating 'caddy' Docker network")
		fmt.Fprintln(w, "  → Starting caddy-docker-proxy")
		if err := caddyMgr.Start(ctx); err != nil {
			return fmt.Errorf("starting Caddy: %w", err)
		}
		fmt.Fprintln(w, "  ✓ Caddy running on :80/:443")
	}

	// Step 8: Register GitHub webhooks
	fmt.Fprintln(w, "\nRegistering GitHub webhooks...")
	token := opts.GitHubToken
	if token == "" {
		token = cfg.Server.GithubToken
	}
	if token != "" {
		ghClient := github.NewGitHubClient(token, slog.Default())

		results, err := github.SyncWebhooks(ctx, cfg, store, ghClient, false)
		if err != nil {
			return fmt.Errorf("syncing webhooks: %w", err)
		}

		// Also register on the server IaC repo itself if not already covered.
		serverResult := syncServerRepoWebhook(ctx, cfg, ghClient, store, opts.ServerRepo)
		results = append(results, serverResult)

		registered := 0
		for _, r := range results {
			suffix := ""
			if r.Repo == opts.ServerRepo {
				suffix = " (self-update)"
			}
			switch r.Action {
			case "created":
				fmt.Fprintf(w, "  → %-24s + created%s\n", r.Repo, suffix)
				registered++
			case "exists":
				fmt.Fprintf(w, "  → %-24s ✓ exists%s\n", r.Repo, suffix)
				registered++
			case "error":
				fmt.Fprintf(w, "  → %-24s ✗ %v\n", r.Repo, r.Error)
			}
		}
		fmt.Fprintf(w, "  ✓ %d webhook%s registered\n", registered, plural(registered))
	} else {
		fmt.Fprintln(w, "  → No GitHub token provided, skipping webhook registration")
	}

	// Step 9: Clone app repositories
	fmt.Fprintln(w, "\nCloning app repositories...")
	appNames := slices.Sorted(maps.Keys(cfg.Apps))
	cloned := 0
	for _, appName := range appNames {
		app := cfg.Apps[appName]
		appRepoDir := filepath.Join(cfg.Server.StacksDir, "apps", appName, "repo")
		fmt.Fprintf(w, "  → %-10s %s:%s → %s\n", appName, app.Repo, app.Branch, appRepoDir)
		if err := os.MkdirAll(filepath.Dir(appRepoDir), 0755); err != nil {
			fmt.Fprintf(w, "    ✗ %v\n", err)
			continue
		}
		if err := git.CloneOrFetch(ctx, token, appRepoDir, git.RepoURL(app.Repo), app.Branch); err != nil {
			fmt.Fprintf(w, "    ✗ %v\n", err)
		} else {
			cloned++
		}
	}
	fmt.Fprintf(w, "  ✓ %d repositor%s cloned\n", cloned, pluralIes(cloned))

	printCompletion(w, cfg, opts)
	return nil
}

// cloneOrPull clones a repo or pulls the latest if already cloned.
func cloneOrPull(ctx context.Context, w io.Writer, repo, destDir, token string) error {
	if _, err := os.Stat(filepath.Join(destDir, ".git")); err == nil {
		fmt.Fprintln(w, "  → Repository already cloned, pulling latest...")
		cmd := git.CmdWithAuth(ctx, token, destDir, "pull")
		var out strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git pull: %w: %s", err, strings.TrimSpace(out.String()))
		}
		fmt.Fprintln(w, "  ✓ Repository updated")
		return nil
	}

	displayURL := fmt.Sprintf("https://github.com/%s.git", repo)
	fmt.Fprintf(w, "  → git clone %s %s\n", displayURL, destDir)

	url := git.RepoURL(repo)
	cmd := git.CmdWithAuth(ctx, token, "", "clone", url, destDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if strings.Contains(stderrStr, "not found") || strings.Contains(stderrStr, "Repository not found") {
			return fmt.Errorf("cannot clone %s. If this is a private repo, provide --github-token", repo)
		}
		if stderrStr != "" {
			return fmt.Errorf("git clone: %w: %s", err, stderrStr)
		}
		return fmt.Errorf("git clone: %w", err)
	}
	fmt.Fprintln(w, "  ✓ Repository cloned")
	return nil
}

// syncServerRepoWebhook registers a webhook on the server IaC repo if not already covered.
func syncServerRepoWebhook(ctx context.Context, cfg *config.Config, client *github.GitHubClient, store *secrets.Store, serverRepo string) github.SyncResult {
	result := github.SyncResult{Repo: serverRepo}

	// Skip if the server repo is already an app repo (handled by SyncWebhooks).
	for _, app := range cfg.Apps {
		if app.Repo == serverRepo {
			result.Action = "exists"
			return result
		}
	}

	webhookSecret, err := store.Get("herald/webhook_secret")
	if err != nil {
		result.Action = "error"
		result.Error = fmt.Errorf("getting webhook secret: %w", err)
		return result
	}

	parts := strings.SplitN(serverRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		result.Action = "error"
		result.Error = fmt.Errorf("invalid repo format %q, expected owner/repo", serverRepo)
		return result
	}
	owner, repo := parts[0], parts[1]

	targetURL := "https://" + cfg.Server.DeployDomain + "/webhook"

	hooks, err := client.ListWebhooks(ctx, owner, repo)
	if err != nil {
		result.Action = "error"
		result.Error = err
		return result
	}

	for _, hook := range hooks {
		if hook.Config.URL == targetURL && hook.Active {
			result.Action = "exists"
			result.ID = hook.ID
			return result
		}
	}

	hook, err := client.CreateWebhook(ctx, owner, repo, github.CreateWebhookRequest{
		URL:    targetURL,
		Secret: webhookSecret,
		Events: []string{"push"},
	})
	if err != nil {
		result.Action = "error"
		result.Error = err
		return result
	}

	result.Action = "created"
	result.ID = hook.ID
	return result
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralIes(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func printCompletion(w io.Writer, cfg *config.Config, opts Options) {
	fmt.Fprintln(w, "\n═══════════════════════════════════════════════════════")
	fmt.Fprintln(w, "Herald initialized successfully!")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Server:     %s\n", cfg.Server.Name)
	fmt.Fprintf(w, "Config:     %s\n", filepath.Join(opts.DataDir, "repo", "config.yml"))
	fmt.Fprintf(w, "Data:       %s\n", opts.DataDir)
	fmt.Fprintf(w, "Stacks:     %s\n", cfg.Server.StacksDir)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. Create env files for your apps:")
	fmt.Fprintln(w, `     herald secret set budget/db_password "your-password"`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  2. Start the herald daemon:")
	fmt.Fprintln(w, "     herald serve")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  3. Deploy your apps:")
	for _, name := range slices.Sorted(maps.Keys(cfg.Apps)) {
		fmt.Fprintf(w, "     herald deploy %s\n", name)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  4. Or deploy all apps:")
	fmt.Fprintln(w, "     herald deploy --all")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  5. Install as a systemd service:")
	fmt.Fprintln(w, "     herald install")
	fmt.Fprintln(w, "═══════════════════════════════════════════════════════")
}
