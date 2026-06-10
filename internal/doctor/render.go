package doctor

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Render writes the diagnosis: a one-line verdict, then healthy checks grouped by
// category, then any problems with their fixes, then the admin-only operational
// inventory and a summary of the last maintenance pass. Layout only — no color.
func (di *Diagnosis) Render(w io.Writer) {
	server := di.Server
	if server == "" {
		server = "unknown"
	}
	fmt.Fprintf(w, "Herald doctor — %s\n\n", server)

	var okN, attnN, warnN int
	for _, c := range di.Checks {
		switch c.Severity {
		case SeverityOK:
			okN++
		case SeverityAttention:
			attnN++
		case SeverityWarning:
			warnN++
		}
	}
	fmt.Fprintf(w, "%s\n", verdict(okN, attnN, warnN))
	if di.StatusURL != "" {
		fmt.Fprintf(w, "Status page: %s\n", di.StatusURL)
	}
	fmt.Fprintln(w)

	di.renderGroupedOK(w)
	renderProblems(w, "Needs attention", di.problems(SeverityAttention))
	renderProblems(w, "Warnings", di.problems(SeverityWarning))
	di.renderInventory(w)
	di.renderLastPass(w)
}

func verdict(ok, attn, warn int) string {
	if attn == 0 && warn == 0 {
		return fmt.Sprintf("✓ Healthy · %d checks passed", ok)
	}
	var parts []string
	if attn > 0 {
		parts = append(parts, fmt.Sprintf("%d need attention", attn))
	}
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", warn))
	}
	glyph := "⚠"
	if attn > 0 {
		glyph = "✗"
	}
	return fmt.Sprintf("%s %s · %d ok", glyph, strings.Join(parts, " · "), ok)
}

// renderGroupedOK prints one line per category listing its passing checks, so the
// healthy majority is a compact block instead of a long flat list.
func (di *Diagnosis) renderGroupedOK(w io.Writer) {
	byCat := map[string][]string{}
	for _, c := range di.Checks {
		if c.Severity == SeverityOK {
			byCat[c.Category] = append(byCat[c.Category], c.Label)
		}
	}
	width := 0
	for _, cat := range categoryOrder {
		if len(byCat[cat]) > 0 && len(cat) > width {
			width = len(cat)
		}
	}
	for _, cat := range categoryOrder {
		labels := byCat[cat]
		if len(labels) == 0 {
			continue
		}
		// Stacks and webhooks are enumerated in the Inventory below, so don't repeat
		// their names here — show only the health signal the inventory lacks.
		var summary string
		switch cat {
		case catStacks:
			summary = fmt.Sprintf("%d/%d running", len(labels), len(di.Stacks))
		case catWebhooks:
			summary = fmt.Sprintf("%d/%d active", len(labels), len(di.Webhooks))
		default:
			summary = strings.Join(labels, " · ")
		}
		fmt.Fprintf(w, "  %-*s   %s\n", width, cat, summary)
	}
}

func (di *Diagnosis) problems(sev Severity) []Check {
	var out []Check
	for _, c := range di.Checks {
		if c.Severity == sev {
			out = append(out, c)
		}
	}
	return out
}

func renderProblems(w io.Writer, heading string, checks []Check) {
	if len(checks) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", heading)
	for _, c := range checks {
		fmt.Fprintf(w, "  %s\n", c.Label)
		if c.Detail != "" {
			fmt.Fprintf(w, "      %s\n", c.Detail)
		}
		if c.Fix != "" {
			fmt.Fprintf(w, "      → %s\n", c.Fix)
		}
	}
}

func (di *Diagnosis) renderInventory(w io.Writer) {
	if len(di.Stacks) == 0 && len(di.Webhooks) == 0 {
		return
	}
	fmt.Fprintf(w, "\nInventory\n")

	whState := map[string]string{}
	for _, wh := range di.Webhooks {
		whState[wh.Repo] = wh.State
	}
	shown := map[string]bool{}

	width := 0
	for _, s := range di.Stacks {
		if n := len(s.Name); n > width {
			width = n
		}
	}
	// One block per stack: a header line with its key facts, then its wiring —
	// webhook, secrets, update hook — nested underneath (names/targets, never values).
	for _, s := range di.Stacks {
		parts := []string{s.Source, s.Domain}
		if s.DeployRef != "" {
			parts = append(parts, s.DeployRef)
		}
		if strings.HasPrefix(s.Source, "path:") {
			if s.AutoDeploy {
				parts = append(parts, "auto-deploy")
			} else {
				parts = append(parts, "manual")
			}
		}
		fmt.Fprintf(w, "  %-*s   %s\n", width, s.Name, strings.Join(parts, " · "))
		if repo, _, ok := strings.Cut(s.Source, "@"); ok {
			if state, found := whState[repo]; found {
				fmt.Fprintf(w, "  %-*s   webhook: %s\n", width, "", state)
				shown[repo] = true
			}
		}
		for _, sec := range s.Secrets {
			fmt.Fprintf(w, "  %-*s   secret: %s\n", width, "", sec)
		}
		if s.Update != "" {
			fmt.Fprintf(w, "  %-*s   update: %s\n", width, "", s.Update)
		}
	}

	// Webhooks for repos not bound to a stack (e.g. the server IaC repo).
	var other []WebhookInventory
	for _, wh := range di.Webhooks {
		if !shown[wh.Repo] {
			other = append(other, wh)
		}
	}
	if len(other) > 0 {
		owidth := 0
		for _, wh := range other {
			if len(wh.Repo) > owidth {
				owidth = len(wh.Repo)
			}
		}
		fmt.Fprintf(w, "\n  Server repo\n")
		for _, wh := range other {
			fmt.Fprintf(w, "  %-*s   webhook: %s\n", owidth, wh.Repo, wh.State)
		}
	}
}

func (di *Diagnosis) renderLastPass(w io.Writer) {
	if di.LastPass == nil {
		return
	}
	r := di.LastPass
	fmt.Fprintf(w, "\nLast maintenance pass\n")
	if !r.FinishedAt.IsZero() {
		fmt.Fprintf(w, "  finished: %s\n", r.FinishedAt.Local().Format(time.RFC3339))
	}
	if r.IaC.NewHEAD != "" {
		if r.IaC.OldHEAD != "" && r.IaC.OldHEAD != r.IaC.NewHEAD {
			fmt.Fprintf(w, "  iac: %s → %s\n", r.IaC.OldHEAD, r.IaC.NewHEAD)
		} else {
			fmt.Fprintf(w, "  iac: %s\n", r.IaC.NewHEAD)
		}
	}
	fmt.Fprintf(w, "  webhooks: %d synced, %d created, %d pruned\n",
		r.Webhooks.Synced, r.Webhooks.Created, r.Webhooks.Pruned)
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "  errors: %d (see last-sync.json)\n", len(r.Errors))
	}
}
