package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/stacks"
	"github.com/nogo/herald/internal/webhook"
	"github.com/spf13/cobra"
)

var port int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start webhook listener and deploy daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := secrets.NewStore(dataDir)
		secret, err := store.Get("herald/webhook_secret")
		if err != nil {
			return fmt.Errorf("webhook secret not configured. Run: herald secret set herald/webhook_secret <your-secret>")
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

		srv := &webhook.Server{
			Config:  Cfg,
			Secret:  secret,
			Verbose: verbose,
			OnDeploy: func(req webhook.DeployRequest) {
				d.DeployAsync(req.AppName)
			},
			IaCRepo:   getIaCRepo(dataDir),
			OnIaCPush: makeIaCPushHandler(stackMgr),
		}

		addr := fmt.Sprintf(":%d", port)
		httpSrv := &http.Server{
			Addr:    addr,
			Handler: srv.Handler(),
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		go func() {
			slog.Info("herald server started", "addr", addr)
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// makeIaCPushHandler returns a function that triggers auto-deploy for stacks
// with auto_deploy: true, and logs the rest.
func makeIaCPushHandler(mgr *stacks.StackManager) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

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
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
}
