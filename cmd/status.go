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

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/preview"
	"github.com/nogo/herald/internal/status"
	"github.com/nogo/herald/internal/ui"
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

		statusURL := ""
		if Cfg != nil && Cfg.Server.DeployDomain != "" {
			statusURL = "https://" + Cfg.Server.DeployDomain
		}
		printStatus(cmd.OutOrStdout(), s, collectContainerStats(ctx), statusURL)
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

// stackGlyph returns a monochrome state glyph (no color, by design).
func stackGlyph(state string) string {
	switch state {
	case "running":
		return "●"
	case "degraded":
		return "◐"
	case "error":
		return "✗"
	default: // stopped, not deployed
		return "○"
	}
}

// stackSummary is the one-line headline shown next to the server name.
func stackSummary(s *status.ServerStatus) string {
	if len(s.Stacks) == 0 {
		return ""
	}
	running := 0
	for _, st := range s.Stacks {
		if st.State == "running" {
			running++
		}
	}
	if running == len(s.Stacks) {
		return fmt.Sprintf("  ·  %d stacks, all running", len(s.Stacks))
	}
	return fmt.Sprintf("  ·  %d stacks, %d running", len(s.Stacks), running)
}

func printStatus(w io.Writer, s *status.ServerStatus, stats map[string]containerStats, statusURL string) {
	// Title + one-line headline.
	fmt.Fprintf(w, "Herald — %s%s\n", s.ServerName, stackSummary(s))
	if statusURL != "" {
		fmt.Fprintf(w, "Status page: %s\n", statusURL)
	}
	fmt.Fprintln(w)

	// Caddy on a single line.
	caddy := "○ stopped"
	if s.Caddy.Running {
		parts := []string{"● running"}
		if s.Caddy.Uptime != "" {
			parts = append(parts, "up "+s.Caddy.Uptime)
		}
		if s.Caddy.DomainCount > 0 {
			parts = append(parts, fmt.Sprintf("%d domains", s.Caddy.DomainCount))
		}
		if s.Caddy.Email != "" {
			parts = append(parts, s.Caddy.Email)
		}
		caddy = strings.Join(parts, " · ")
	}
	fmt.Fprintf(w, "Caddy    %s\n", caddy)

	// Stacks.
	if len(s.Stacks) > 0 {
		fmt.Fprintf(w, "\nStacks\n")
		tbl := ui.NewTable("  ", "NAME", "STATUS", "CONT", "CPU", "MEM", "DOMAIN", "SOURCE", "REF").
			RightAlign(2, 3, 4)
		for _, st := range s.Stacks {
			ref := st.DeployedRef
			if strings.HasPrefix(ref, "refs/tags/") {
				ref = "tag:" + strings.TrimPrefix(ref, "refs/tags/")
			}
			if ref == "" {
				ref = "-"
			}
			cont, cpu, mem := "-", "-", "-"
			if st.State != "not deployed" {
				cont = fmt.Sprintf("%d/%d", st.ContainersUp, st.ContainersTotal)
				if cs, ok := stats["herald-"+st.Name]; ok {
					cpu = fmt.Sprintf("%.1f%%", cs.cpuPercent)
					mem = humanizeBytes(cs.memBytes)
				}
			}
			tbl.Row(st.Name, stackGlyph(st.State)+" "+st.State, cont, cpu, mem, st.Domain, st.Source, ref)
		}
		tbl.Render(w)
	}

	// Previews.
	if len(s.Previews) > 0 {
		fmt.Fprintf(w, "\nPreviews\n")
		tbl := ui.NewTable("  ", "APP", "DOMAIN", "BRANCH", "AGE")
		for _, p := range s.Previews {
			tbl.Row(p.AppName, p.Domain, p.Branch, p.Age)
		}
		tbl.Render(w)
	}

	// Webhooks.
	if len(s.Webhooks) > 0 {
		header := "\nWebhooks"
		if !s.WebhookSyncedAt.IsZero() {
			header += "   synced " + s.WebhookSyncedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\n", header)
		tbl := ui.NewTable("  ", "REPO", "STATUS")
		for _, wh := range s.Webhooks {
			whStatus := "✗ not registered"
			switch {
			case wh.Unknown:
				whStatus = "? unknown"
			case wh.Registered:
				whStatus = "● registered"
			}
			tbl.Row(wh.Repo, whStatus)
		}
		tbl.Render(w)
	}

	if statusHasIssues(s) {
		fmt.Fprintln(w, "\nIssues detected — run 'herald doctor' to diagnose.")
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
