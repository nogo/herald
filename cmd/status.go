package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/status"
	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show all services, domains, and health",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		caddyMgr := &caddy.CaddyManager{
			Config:     Cfg,
			Logger:     slog.Default(),
			HeraldPort: port,
		}

		previewMgr := &preview.PreviewManager{
			Config:  Cfg,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		collector := &status.StatusCollector{
			Config:  Cfg,
			DataDir: dataDir,
			Logger:  slog.Default(),
			Caddy:   caddyMgr,
			Preview: previewMgr,
		}

		ctx := context.Background()
		s, err := collector.Collect(ctx)
		if err != nil {
			return err
		}

		if statusJSON {
			data, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		}

		printStatus(cmd.OutOrStdout(), s, collectContainerStats(ctx))
		return nil
	},
}

// containerStats holds CPU and memory aggregated across a stack's containers.
type containerStats struct {
	cpuPercent float64
	memBytes   int64
}

// collectContainerStats returns live CPU/memory aggregated per compose project
// (keyed "herald-<stack>"). It is best-effort: any failure yields a nil map and
// the caller renders no CPU/mem rather than failing the status command. Two
// docker calls total, independent of stack count: `docker ps` to map each
// container to its compose project, then `docker stats --no-stream`.
func collectContainerStats(ctx context.Context) map[string]containerStats {
	psOut, err := exec.CommandContext(ctx, "docker", "ps",
		"--format", "{{.ID}}\t{{.Label \"com.docker.compose.project\"}}").Output()
	if err != nil {
		return nil
	}
	idToProject := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
		id, project, ok := strings.Cut(line, "\t")
		if !ok || project == "" {
			continue
		}
		idToProject[id] = project
	}
	if len(idToProject) == 0 {
		return nil
	}

	statsOut, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
		"--format", "{{.ID}}\t{{.CPUPerc}}\t{{.MemUsage}}").Output()
	if err != nil {
		return nil
	}
	agg := make(map[string]containerStats)
	for _, line := range strings.Split(strings.TrimSpace(string(statsOut)), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		project, ok := idToProject[fields[0]]
		if !ok {
			continue
		}
		cur := agg[project]
		cur.cpuPercent += parseCPUPerc(fields[1])
		cur.memBytes += parseMemUsage(fields[2])
		agg[project] = cur
	}
	return agg
}

// parseCPUPerc parses docker stats CPUPerc like "12.34%" into 12.34.
func parseCPUPerc(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMemUsage parses the used side of docker stats MemUsage like
// "45.6MiB / 1.9GiB" into bytes.
func parseMemUsage(s string) int64 {
	used, _, _ := strings.Cut(s, "/")
	return parseSize(strings.TrimSpace(used))
}

// parseSize parses a docker size string like "45.6MiB" or "512B" into bytes.
func parseSize(s string) int64 {
	units := []struct {
		suffix string
		mult   float64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil {
				return 0
			}
			return int64(v * u.mult)
		}
	}
	return 0
}

// humanizeBytes formats a byte count as a compact binary size, e.g. "45.6MiB".
func humanizeBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func stateIcon(state string) string {
	switch state {
	case "running":
		return "● running"
	case "degraded":
		return "◐ degraded"
	case "stopped":
		return "○ stopped"
	case "not deployed":
		return "○ not deployed"
	case "error":
		return "✗ error"
	default:
		return "? " + state
	}
}

func printStatus(w io.Writer, s *status.ServerStatus, stats map[string]containerStats) {
	title := fmt.Sprintf("Herald Status — %s", s.ServerName)
	sep := strings.Repeat("═", len([]rune(title))+4)
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, sep)
	fmt.Fprintln(w)

	// Caddy.
	caddyLine := "○ stopped"
	if s.Caddy.Running {
		caddyLine = "● running"
		if s.Caddy.Uptime != "" {
			caddyLine += " (up " + s.Caddy.Uptime + ")"
		}
	}
	fmt.Fprintf(w, "Caddy: %s\n", caddyLine)
	if s.Caddy.Email != "" {
		fmt.Fprintf(w, "  ACME: %s\n", s.Caddy.Email)
	}
	if s.Caddy.DomainCount > 0 {
		fmt.Fprintf(w, "  Domains: %d active\n", s.Caddy.DomainCount)
	}

	// Stacks.
	if len(s.Stacks) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Stacks:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Name\tDomain\tStatus\tContainers\tCPU\tMem\tSource\tRef")
		for _, st := range s.Stacks {
			ref := st.DeployedRef
			if strings.HasPrefix(ref, "refs/tags/") {
				ref = "tag:" + strings.TrimPrefix(ref, "refs/tags/")
			}
			if ref == "" {
				ref = "-"
			}
			containers, cpu, mem := "", "-", "-"
			if st.State != "not deployed" {
				containers = fmt.Sprintf("%d/%d", st.ContainersUp, st.ContainersTotal)
				if cs, ok := stats["herald-"+st.Name]; ok {
					cpu = fmt.Sprintf("%.1f%%", cs.cpuPercent)
					mem = humanizeBytes(cs.memBytes)
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				st.Name, st.Domain, stateIcon(st.State), containers, cpu, mem, st.Source, ref)
		}
		tw.Flush()
	}

	// Previews.
	if len(s.Previews) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Previews:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  App\tDomain\tBranch\tAge")
		for _, p := range s.Previews {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.AppName, p.Domain, p.Branch, p.Age)
		}
		tw.Flush()
	}

	// Webhooks.
	if len(s.Webhooks) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Webhooks:")
		if !s.WebhookSyncedAt.IsZero() {
			fmt.Fprintf(w, "  (last synced: %s)\n", s.WebhookSyncedAt.Local().Format(time.RFC3339))
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Repo\tStatus")
		for _, wh := range s.Webhooks {
			var whStatus string
			switch {
			case wh.Unknown:
				whStatus = "? unknown"
			case wh.Registered:
				whStatus = "● registered"
			default:
				whStatus = "✗ not registered"
			}
			fmt.Fprintf(tw, "  %s\t%s\n", wh.Repo, whStatus)
		}
		tw.Flush()
	}

	if statusHasIssues(s) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Issues detected — run 'herald doctor' to diagnose.")
	}
}

// statusHasIssues reports whether the collected status shows anything that
// warrants a closer look, gating the doctor hint so it stays signal not noise.
func statusHasIssues(s *status.ServerStatus) bool {
	if !s.Caddy.Running {
		return true
	}
	for _, st := range s.Stacks {
		switch st.State {
		case "running":
		default: // stopped, degraded, error, not deployed
			return true
		}
	}
	for _, wh := range s.Webhooks {
		if wh.Unknown || !wh.Registered {
			return true
		}
	}
	return false
}

func init() {
	statusCmd.GroupID = "daemon"
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output as JSON")
}
