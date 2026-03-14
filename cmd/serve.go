package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nogo/herald/internal/deployer"
	"github.com/nogo/herald/internal/secrets"
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

		srv := &webhook.Server{
			Config:  Cfg,
			Secret:  secret,
			Verbose: verbose,
			OnDeploy: func(req webhook.DeployRequest) {
				d.DeployAsync(req.AppName)
			},
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

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
}
