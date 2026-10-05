package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/nogo/herald/internal/config"
	"github.com/spf13/cobra"
)

var signalPort int

var signalCmd = &cobra.Command{
	Use:   "signal",
	Short: "Ask the local daemon to sync the server repo",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		port := signalPort
		if !cmd.Flags().Changed("port") {
			port = daemonPort(dataDir)
		}
		if err := signalSync(cmd.Context(), port); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "sync submitted")
		return nil
	},
}

// daemonPort returns server.port from the server clone's config.yml, the file
// the daemon loaded at startup. A missing or invalid config falls back to the
// default port, so the hook still reaches a daemon that runs without one.
func daemonPort(dataDir string) int {
	cfg, err := config.Load(filepath.Join(dataDir, "repo", "config.yml"))
	if err != nil {
		return config.DefaultPort
	}
	return cfg.Server.Port
}

// signalSync bypasses HTTP proxies: a post-receive hook must reach the daemon
// directly on loopback, regardless of the operator's proxy environment.
func signalSync(ctx context.Context, port int) error {
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		Timeout:       5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	url := fmt.Sprintf("http://127.0.0.1:%d/sync", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("creating sync signal: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("signalling local daemon at %s: %w; check herald serve is running and --port matches its listener", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return fmt.Errorf("reading sync signal response: %w", err)
		}
		return fmt.Errorf("sync signal rejected: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func init() {
	signalCmd.GroupID = "daemon"
	rootCmd.AddCommand(signalCmd)
	signalCmd.Flags().IntVar(&signalPort, "port", 0, fmt.Sprintf("Local daemon port (default: server.port from the server repo's config.yml, else %d)", config.DefaultPort))
}
