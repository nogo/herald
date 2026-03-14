package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nogo/herald/internal/caddy"
	"github.com/spf13/cobra"
)

var caddyCmd = &cobra.Command{
	Use:   "caddy",
	Short: "Manage the Caddy reverse proxy",
}

var caddyStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Caddy reverse proxy",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		heraldPort, _ := cmd.Flags().GetInt("herald-port")
		m := &caddy.CaddyManager{
			Config:     Cfg,
			Logger:     slog.Default(),
			HeraldPort: heraldPort,
		}
		return m.Start(context.Background())
	},
}

var caddyStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Caddy reverse proxy",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		m := &caddy.CaddyManager{
			Config: Cfg,
			Logger: slog.Default(),
		}
		return m.Stop(context.Background())
	},
}

var caddyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Caddy status and proxied domains",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		m := &caddy.CaddyManager{
			Config: Cfg,
			Logger: slog.Default(),
		}
		s, err := m.Status(context.Background())
		if err != nil {
			return err
		}
		if s.Running {
			fmt.Printf("Caddy: running (up %s)\n", s.Uptime)
		} else {
			fmt.Println("Caddy: stopped")
		}
		fmt.Printf("ACME email: %s\n", s.Email)
		if len(s.Domains) > 0 {
			fmt.Println("\nProxied domains:")
			maxLen := 0
			for _, d := range s.Domains {
				if len(d.Domain) > maxLen {
					maxLen = len(d.Domain)
				}
			}
			for _, d := range s.Domains {
				pad := strings.Repeat(" ", maxLen-len(d.Domain))
				fmt.Printf("  %s%s  → %s\n", d.Domain, pad, d.Upstream)
			}
		}
		return nil
	},
}

func init() {
	caddyStartCmd.Flags().Int("herald-port", 9483, "Port where herald's webhook server is running")
	caddyCmd.AddCommand(caddyStartCmd, caddyStopCmd, caddyStatusCmd)
	rootCmd.AddCommand(caddyCmd)
}
