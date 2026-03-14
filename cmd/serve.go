package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/deployer"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/stacks"
	"github.com/nogo/herald/internal/status"
	"github.com/nogo/herald/internal/web"
	"github.com/nogo/herald/internal/webhook"
	"github.com/spf13/cobra"
)

var port int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start webhook listener and deploy daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		// Resolve port: CLI flag overrides config.
		listenPort := Cfg.Server.Port
		if cmd.Flags().Changed("port") {
			listenPort = port
		}

		store := secrets.NewStore(dataDir)
		secret, err := store.Get("herald/webhook_secret")
		if err != nil {
			return fmt.Errorf("webhook secret not configured. Run: herald secret set herald/webhook_secret")
		}

		d := &deployer.Deployer{
			Config:  Cfg,
			Secrets: store,
			Logger:  slog.Default(),
		}

		stackMgr := &stacks.StackManager{
			Config:  Cfg,
			Secrets: store,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		previewMgr := &preview.PreviewManager{
			Config:  Cfg,
			Secrets: store,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		// Set up status page if password is configured.
		var webHandler *web.WebHandler
		if statusPass, err := store.Get("herald/status_password"); err == nil {
			caddyMgr := &caddy.CaddyManager{
				Config:     Cfg,
				Logger:     slog.Default(),
				HeraldPort: listenPort,
			}
			collector := &status.StatusCollector{
				Config:  Cfg,
				DataDir: dataDir,
				Logger:  slog.Default(),
				Caddy:   caddyMgr,
				Preview: previewMgr,
			}
			webHandler = web.NewWebHandler(collector, Cfg, statusPass, slog.Default())
			if webHandler != nil {
				slog.Info("status page enabled", "domain", Cfg.Server.DeployDomain)
			}
		} else {
			slog.Info("status page disabled: run 'herald secret set herald/status_password <password>' to enable")
		}

		srv := &webhook.Server{
			Config:  Cfg,
			Secret:  secret,
			Verbose: verbose,
			Web:     webHandler,
			OnDeploy: func(req webhook.DeployRequest) {
				d.DeployAsync(req.AppName)
			},
			IaCRepo:   getIaCRepo(dataDir),
			OnIaCPush: makeIaCPushHandler(stackMgr),
			OnPreviewDeploy: func(appName, branch, commit string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := previewMgr.Deploy(ctx, appName, branch, commit); err != nil {
					slog.Error("preview deploy failed", "app", appName, "branch", branch, "error", err)
				}
			},
			OnPreviewTeardown: func(appName, branch string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := previewMgr.Teardown(ctx, appName, branch); err != nil {
					slog.Error("preview teardown failed", "app", appName, "branch", branch, "error", err)
				}
			},
		}

		addr := fmt.Sprintf(":%d", listenPort)
		httpSrv := &http.Server{
			Addr:    addr,
			Handler: srv.Handler(),
		}

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		slog.Info("herald ready, listening on", "addr", addr)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		go func() {
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("server error", "error", err)
				stop()
			}
		}()

		<-ctx.Done()
		stop()

		if cause := context.Cause(ctx); cause != nil {
			slog.Info("herald server stopping", "cause", cause)
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}

		d.Wait()
		slog.Info("herald server stopped")
		return nil
	},
}

// getIaCRepo reads the git remote URL from the IaC repo clone and returns the
// GitHub full name (e.g. "nogo/srv2"), or "" if unavailable.
func getIaCRepo(dataDir string) string {
	repoDir := filepath.Join(dataDir, "repo")
	out, err := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return parseGitHubRepo(strings.TrimSpace(string(out)))
}

// parseGitHubRepo extracts "owner/repo" from a GitHub clone URL.
func parseGitHubRepo(rawURL string) string {
	rawURL = strings.TrimSuffix(rawURL, ".git")
	if after, ok := strings.CutPrefix(rawURL, "https://github.com/"); ok {
		return after
	}
	// git@github.com:owner/repo
	if idx := strings.Index(rawURL, "@github.com:"); idx >= 0 {
		return rawURL[idx+len("@github.com:"):]
	}
	return ""
}

// makeIaCPushHandler returns a function that pulls the IaC repo, reloads config,
// and triggers auto-deploy for stacks with auto_deploy: true.
func makeIaCPushHandler(mgr *stacks.StackManager) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		// 1. Pull the latest IaC repo.
		repoDir := filepath.Join(dataDir, "repo")
		slog.Info("IaC push: pulling latest config")
		token := resolveToken()
		if output, err := githelper.PullFFOnly(token, repoDir); err != nil {
			slog.Error("IaC push: git pull failed", "error", err, "output", output)
			return
		}

		// 2. Reload config.
		newCfg, err := LoadConfigWithToken(cfgFile, dataDir)
		if err != nil {
			slog.Error("IaC push: config reload failed", "error", err)
			return
		}
		Cfg = newCfg
		mgr.Config = newCfg
		slog.Info("IaC push: config reloaded")

		// 3. Auto-deploy stacks.
		for _, info := range mgr.List() {
			if info.AutoDeploy {
				slog.Info("IaC push: auto-deploying stack", "stack", info.Name)
				if err := mgr.ComposeUp(ctx, info.Name); err != nil {
					slog.Error("IaC push: auto-deploy failed", "stack", info.Name, "error", err)
				} else {
					slog.Info("IaC push: auto-deploy complete", "stack", info.Name)
				}
			} else {
				slog.Info("IaC push: stack updated in repo (no auto-deploy)", "stack", info.Name)
			}
		}
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().IntVar(&port, "port", 9483, "Port to listen on")
}
