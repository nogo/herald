package doctor

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Render writes the diagnosis as grouped, actionable text: the diagnosis first
// (OK / Needs attention / Warnings, each failure carrying a fix), then the
// admin-only operational inventory, then a one-line summary of the last
// maintenance pass.
func (di *Diagnosis) Render(w io.Writer) {
	server := di.Server
	if server == "" {
		server = "unknown"
	}
	title := "Herald doctor — " + server
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, strings.Repeat("═", len([]rune(title))+4))

	var oks, attention, warnings []Check
	for _, c := range di.Checks {
		switch c.Severity {
		case SeverityOK:
			oks = append(oks, c)
		case SeverityAttention:
			attention = append(attention, c)
		case SeverityWarning:
			warnings = append(warnings, c)
		}
	}

	if len(oks) > 0 {
		fmt.Fprintln(w, "\nOK:")
		for _, c := range oks {
			fmt.Fprintf(w, "  %s\n", c.Name)
		}
	}

	renderProblems(w, "Needs attention", attention)
	renderProblems(w, "Warnings", warnings)

	if len(attention) == 0 && len(warnings) == 0 {
		fmt.Fprintln(w, "\nNo problems detected.")
	}

	di.renderInventory(w)
	di.renderLastPass(w)
}

func renderProblems(w io.Writer, heading string, checks []Check) {
	if len(checks) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", heading)
	for _, c := range checks {
		fmt.Fprintf(w, "  %s\n", c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, "    %s\n", c.Detail)
		}
		if c.Fix != "" {
			fmt.Fprintf(w, "    fix: %s\n", c.Fix)
		}
	}
}

func (di *Diagnosis) renderInventory(w io.Writer) {
	if len(di.Stacks) == 0 && len(di.Webhooks) == 0 {
		return
	}
	fmt.Fprintln(w, "\nInventory:")

	if len(di.Stacks) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  Stack\tSource\tDomain\tDeployed\tAuto")
		for _, s := range di.Stacks {
			ref := s.DeployRef
			if ref == "" {
				ref = "-"
			}
			auto := "-"
			if strings.HasPrefix(s.Source, "path:") {
				if s.AutoDeploy {
					auto = "yes"
				} else {
					auto = "no"
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", s.Name, s.Source, s.Domain, ref, auto)
		}
		tw.Flush()

		// Secret targets and update scripts, listed per stack (names only).
		for _, s := range di.Stacks {
			if len(s.Secrets) == 0 && s.Update == "" {
				continue
			}
			fmt.Fprintf(w, "  %s:\n", s.Name)
			for _, sec := range s.Secrets {
				fmt.Fprintf(w, "    secret: %s\n", sec)
			}
			if s.Update != "" {
				fmt.Fprintf(w, "    update: %s\n", s.Update)
			}
		}
	}

	if len(di.Webhooks) > 0 {
		fmt.Fprintln(w, "\n  Webhooks:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, wh := range di.Webhooks {
			fmt.Fprintf(tw, "    %s\t%s\n", wh.Repo, wh.State)
		}
		tw.Flush()
	}
}

func (di *Diagnosis) renderLastPass(w io.Writer) {
	if di.LastPass == nil {
		return
	}
	r := di.LastPass
	fmt.Fprintln(w, "\nLast maintenance pass:")
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
