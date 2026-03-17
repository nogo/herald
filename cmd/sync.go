package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/github"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/services"
	"github.com/nogo/herald/internal/status"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Reconcile config with running state",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ctx := context.Background()
		out := cmd.OutOrStdout()

		store := secrets.NewStore(dataDir)

		// 1. Pull IaC repo.
		repoDir := filepath.Join(dataDir, "repo")
		if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
			token := resolveToken()
			if output, pullErr := githelper.PullFFOnly(ctx, token, repoDir); pullErr != nil {
				fmt.Fprintf(os.Stderr, "warning: git pull failed: %s\n", output)
			}
		}

		// 2. Reload config (with secrets store token fallback).
		cfg, err := LoadConfigWithToken(cfgFile, dataDir)
		if err != nil {
			return fmt.Errorf("reloading config: %w", err)
		}
		Cfg = cfg

		caddyMgr := &caddy.CaddyManager{
			Config:     cfg,
			Logger:     slog.Default(),
			HeraldPort: port,
		}

		stackMgr := &services.ServiceManager{
			Config:  cfg,
			Secrets: store,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		// 3. Ensure Caddy is running.
		caddyStatus := "running"
		running, err := caddyMgr.IsRunning(ctx)
		if err != nil {
			caddyStatus = fmt.Sprintf("error checking: %v", err)
		} else if !running {
			if startErr := caddyMgr.Start(ctx); startErr != nil {
				caddyStatus = fmt.Sprintf("failed to start: %v", startErr)
			} else {
				caddyStatus = "started"
			}
		}

		// 4. Sync webhooks.
		webhookSynced, webhookCreated, webhookRemoved := 0, 0, 0
		webhookEnabled := cfg.Server.GithubToken != ""
		if webhookEnabled {
			client := github.NewGitHubClient(cfg.Server.GithubToken, slog.Default())
			results, syncErr := github.SyncWebhooks(ctx, cfg, store, client, false)
			if syncErr != nil {
				fmt.Fprintf(os.Stderr, "warning: webhook sync failed: %v\n", syncErr)
			} else {
				ws := &status.WebhookState{
					SyncedAt: time.Now().UTC(),
					Repos:    make(map[string]status.WebhookEntry),
				}
				for _, r := range results {
					switch r.Action {
					case "exists":
						webhookSynced++
						ws.Repos[r.Repo] = status.WebhookEntry{ID: r.ID, Registered: true}
					case "created":
						webhookCreated++
						ws.Repos[r.Repo] = status.WebhookEntry{ID: r.ID, Registered: true}
					case "removed":
						webhookRemoved++
					case "error":
						fmt.Fprintf(os.Stderr, "  webhook error for %s: %v\n", r.Repo, r.Error)
					}
				}
				wsPath := status.WebhookStatePath(dataDir)
				if saveErr := status.SaveWebhookState(wsPath, ws); saveErr != nil {
					fmt.Fprintf(os.Stderr, "warning: saving webhook state: %v\n", saveErr)
				}
			}
		}

		// 5. Check each app.
		appNames := slices.Sorted(maps.Keys(cfg.Apps))
		appsDeployed, appsNotDeployed := 0, 0
		var pendingActions []string

		for _, name := range appNames {
			app := cfg.Apps[name]
			appDir := filepath.Join(cfg.Server.StacksDir, "apps", name)
			if _, err := os.Stat(appDir); os.IsNotExist(err) {
				appsNotDeployed++
				pendingActions = append(pendingActions,
					fmt.Sprintf("  → Run 'herald deploy %s' to deploy (not yet deployed)", name))
				continue
			}
			appsDeployed++

			// Check Docker running state.
			psOut, psErr := exec.CommandContext(ctx, "docker", "compose",
				"-p", "herald-"+name, "ps", "--format", "json").Output()
			trimmed := strings.TrimSpace(string(psOut))
			if psErr != nil || trimmed == "" || trimmed == "[]" {
				slog.Warn("app is not running", "app", name)
				continue
			}

			// Check for pending commits (compare local HEAD vs remote).
			repoDir := filepath.Join(appDir, "repo")
			localCmd := githelper.CmdWithAuth(ctx, "", repoDir, "rev-parse", "--short", "HEAD")
			localOut, localErr := localCmd.Output()
			localCommit := strings.TrimSpace(string(localOut))
			if localErr != nil {
				continue
			}
			token := resolveToken()
			lsCmd := githelper.CmdWithAuth(ctx, token, repoDir, "ls-remote", "origin", app.Branch)
			lsOut, remoteErr := lsCmd.Output()
			lsRemote := strings.TrimSpace(string(lsOut))
			if remoteErr != nil {
				continue
			}
			// ls-remote output: "<sha>\trefs/heads/<branch>"
			fields := strings.Fields(lsRemote)
			if len(fields) == 0 {
				continue
			}
			remoteSHA := fields[0]
			if len(remoteSHA) > 7 {
				remoteSHA = remoteSHA[:7]
			}
			if localCommit != "" && remoteSHA != "" && localCommit != remoteSHA {
				pendingActions = append(pendingActions,
					fmt.Sprintf("  → App '%s' has new commits (deployed: %s, remote: %s)",
						name, localCommit, remoteSHA))
			}
		}

		// 6. Check each service.
		stackNames := slices.Sorted(maps.Keys(cfg.Services))
		stacksRunning := 0

		for _, name := range stackNames {
			deployDir := filepath.Join(cfg.Server.StacksDir, "stacks", name)
			repoLink := filepath.Join(deployDir, "repo")
			overrideFile := filepath.Join(deployDir, "compose.override.yml")

			isSetUp := false
			if _, err1 := os.Lstat(repoLink); err1 == nil {
				if _, err2 := os.Lstat(overrideFile); err2 == nil {
					isSetUp = true
				}
			}

			if !isSetUp {
				slog.Info("service not set up, configuring", "service", name)
				if setupErr := stackMgr.Setup(ctx, name); setupErr != nil {
					slog.Error("service setup failed", "service", name, "error", setupErr)
					continue
				}
			}

			psOut, psErr := exec.CommandContext(ctx, "docker", "compose",
				"-p", "herald-stack-"+name, "ps", "--format", "json").Output()
			trimmed := strings.TrimSpace(string(psOut))
			if psErr != nil || trimmed == "" || trimmed == "[]" {
				slog.Warn("service is stopped", "service", name)
			} else {
				stacksRunning++
			}
		}

		// 7. Orphan detection.
		orphans := detectOrphans(ctx, cfg)
		for _, o := range orphans {
			fmt.Fprintf(os.Stderr, "orphan detected: %s (not in config)\n", o)
		}

		// Print summary.
		fmt.Fprintln(out, "Sync complete:")
		fmt.Fprintln(out, "  Config reloaded")
		fmt.Fprintf(out, "  Caddy: %s\n", caddyStatus)
		if webhookEnabled {
			fmt.Fprintf(out, "  Webhooks: %d synced, %d created, %d removed\n",
				webhookSynced, webhookCreated, webhookRemoved)
		} else {
			fmt.Fprintln(out, "  Webhooks: skipped (no GitHub token configured)")
		}
		fmt.Fprintf(out, "  Apps: %d configured, %d deployed, %d not deployed\n",
			len(appNames), appsDeployed, appsNotDeployed)
		fmt.Fprintf(out, "  Services: %d configured, %d running\n",
			len(stackNames), stacksRunning)
		fmt.Fprintf(out, "  Orphans: %d\n", len(orphans))

		if len(pendingActions) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Pending actions:")
			for _, a := range pendingActions {
				fmt.Fprintln(out, a)
			}
		}

		// 8. Advisory: warn about missing required secrets (non-blocking).
		var warnLines []string
		for _, name := range appNames {
			app := cfg.Apps[name]
			missing, merr := store.MissingRequired(app.Secrets)
			if merr != nil {
				fmt.Fprintf(os.Stderr, "warning: checking secrets for app %q: %v\n", name, merr)
				continue
			}
			if len(missing) > 0 {
				warnLines = append(warnLines, fmt.Sprintf("  app %q: %s", name, strings.Join(missing, ", ")))
			}
		}
		for _, name := range stackNames {
			svc := cfg.Services[name]
			missing, merr := store.MissingRequired(svc.Secrets)
			if merr != nil {
				fmt.Fprintf(os.Stderr, "warning: checking secrets for service %q: %v\n", name, merr)
				continue
			}
			if len(missing) > 0 {
				warnLines = append(warnLines, fmt.Sprintf("  service %q: %s", name, strings.Join(missing, ", ")))
			}
		}
		if len(warnLines) > 0 {
			fmt.Fprintln(os.Stderr, "\nWarning: the following secrets are required but not set:")
			for _, line := range warnLines {
				fmt.Fprintln(os.Stderr, line)
			}
			fmt.Fprintln(os.Stderr, "Run `herald secret set <key>` to set them before deploying.")
		}

		return nil
	},
}

// detectOrphans lists Docker Compose projects with the "herald-" prefix that
// are not accounted for by the current config.
func detectOrphans(ctx context.Context, cfg *config.Config) []string {
	type projectInfo struct {
		Name string `json:"Name"`
	}

	out, err := exec.CommandContext(ctx, "docker", "compose", "ls",
		"--format", "json", "--all").Output()
	if err != nil {
		return nil
	}

	var projects []projectInfo
	if err := json.Unmarshal(out, &projects); err != nil {
		return nil
	}

	known := map[string]bool{
		"herald-caddy": true,
	}
	for name := range cfg.Apps {
		known["herald-"+name] = true
	}
	for name := range cfg.Services {
		known["herald-stack-"+name] = true
	}

	var orphans []string
	for _, p := range projects {
		if !strings.HasPrefix(p.Name, "herald-") {
			continue
		}
		if strings.HasPrefix(p.Name, "herald-preview-") {
			continue // previews are managed separately
		}
		if !known[p.Name] {
			orphans = append(orphans, p.Name)
		}
	}
	return orphans
}

// syncCmdOutput runs a command in dir and returns trimmed stdout.
func syncCmdOutput(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
