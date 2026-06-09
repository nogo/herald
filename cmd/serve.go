package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/deployer"
	githelper "github.com/nogo/herald/internal/git"
	"github.com/nogo/herald/internal/maintenance"
	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/secrets"
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

		// live is the authoritative config shared by every daemon component. The
		// maintenance pass publishes reloads here so handlers never race a swap.
		live := &atomic.Pointer[config.Config]{}
		live.Store(Cfg)

		d := &deployer.Deployer{
			Config:     Cfg,
			LiveConfig: live,
			Secrets:    store,
			Logger:     slog.Default(),
			DataDir:    dataDir,
		}

		previewMgr := &preview.PreviewManager{
			Config:     Cfg,
			LiveConfig: live,
			Secrets:    store,
			DataDir:    dataDir,
			Logger:     slog.Default(),
		}

		// Set up status page if password is configured.
		var webHandler *web.WebHandler
		if statusPass, err := store.Get("herald/status_password"); err == nil && statusPass != "" {
			caddyMgr := &caddy.CaddyManager{
				Config:     Cfg,
				Logger:     slog.Default(),
				HeraldPort: listenPort,
			}
			collector := &status.StatusCollector{
				Config:     Cfg,
				LiveConfig: live,
				DataDir:    dataDir,
				Logger:     slog.Default(),
				Caddy:      caddyMgr,
				Preview:    previewMgr,
			}
			webHandler = web.NewWebHandler(collector, Cfg, statusPass, slog.Default())
			if webHandler != nil {
				slog.Info("status page enabled", "domain", Cfg.Server.DeployDomain)
			}
		} else {
			slog.Info("status page disabled: run 'herald secret set herald/status_password <password>' to enable")
		}

		runner := &maintenance.Runner{
			DataDir:    dataDir,
			Logger:     slog.Default(),
			Secrets:    store,
			Deployer:   d,
			Live:       live,
			Reload:     func() (*config.Config, error) { return LoadConfigWithToken(cfgFile, dataDir) },
			IaCRepo:    getIaCRepo(dataDir),
			HeraldPort: listenPort,
		}

		srv := &webhook.Server{
			Config:     Cfg,
			LiveConfig: live,
			Secret:     secret,
			Verbose:    verbose,
			Web:        webHandler,
			OnDeploy: func(req webhook.DeployRequest) {
				d.DeployAsync(req.StackName, req.Ref)
			},
			IaCRepo: getIaCRepo(dataDir),
			OnIaCPush: func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				rep := runner.Run(ctx, maintenance.Options{
					Pull:            true,
					Webhooks:        maintenance.ReconcileDelta,
					RedeployChanged: true,
				})
				slog.Info("IaC push: maintenance pass complete",
					"webhooks_created", rep.Webhooks.Created,
					"webhooks_pruned", rep.Webhooks.Pruned,
					"orphans", len(rep.Orphans))
			},
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

		addr := fmt.Sprintf("%s:%d", Cfg.Server.Bind, listenPort)
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

		// Startup maintenance pass: recover any server-repo changes missed while
		// the daemon was offline. Runs in the background so the webhook endpoint is
		// immediately responsive.
		go func() {
			mctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			rep := runner.Run(mctx, maintenance.Options{
				Pull:            true,
				Webhooks:        maintenance.ReconcileFull,
				RedeployChanged: true,
			})
			slog.Info("startup maintenance complete",
				"caddy", rep.Caddy,
				"webhooks_synced", rep.Webhooks.Synced,
				"webhooks_created", rep.Webhooks.Created,
				"webhooks_pruned", rep.Webhooks.Pruned,
				"orphans", len(rep.Orphans))
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
	cmd := githelper.CmdWithAuth(context.Background(), "", repoDir, "remote", "get-url", "origin")
	out, err := cmd.Output()
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

// effectivePort returns the port Herald listens on: the configured port, or the
// default if unset. Used to generate Caddy's herald upstream when ensuring Caddy.
func effectivePort() int {
	if Cfg != nil && Cfg.Server.Port != 0 {
		return Cfg.Server.Port
	}
	return 9483
}

func init() {
	serveCmd.GroupID = "daemon"
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().IntVar(&port, "port", 9483, "Port to listen on")
}
