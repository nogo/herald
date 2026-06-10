package doctor

import (
	"strings"
	"testing"

	"github.com/nogo/herald/internal/maintenance"
)

func TestRenderHealthyGroupsByCategory(t *testing.T) {
	di := &Diagnosis{
		Server: "srv1",
		Checks: []Check{
			{Category: catSystem, Label: "docker", Severity: SeverityOK},
			{Category: catSystem, Label: "git", Severity: SeverityOK},
			{Category: catCaddy, Label: "running", Severity: SeverityOK},
		},
	}
	var sb strings.Builder
	di.Render(&sb)
	out := sb.String()

	for _, want := range []string{
		"Herald doctor — srv1",
		"✓ Healthy · 3 checks passed",
		"System",
		"docker · git",
		"Caddy",
		"running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	// Healthy output has no problem sections.
	if strings.Contains(out, "Needs attention") || strings.Contains(out, "Warnings") {
		t.Errorf("healthy diagnosis should not show problem sections:\n%s", out)
	}
}

func TestRenderProblemsWithFixAndVerdict(t *testing.T) {
	di := &Diagnosis{
		Server: "srv1",
		Checks: []Check{
			{Category: catSystem, Label: "docker", Severity: SeverityOK},
			{Category: catStacks, Label: "blog: missing required secret", Severity: SeverityAttention,
				Detail: "blog/db_password", Fix: "herald secret set blog/db_password"},
			{Category: catStacks, Label: "old-wiki: orphan", Severity: SeverityWarning,
				Detail: "running but not in config", Fix: "herald down old-wiki"},
		},
	}
	var sb strings.Builder
	di.Render(&sb)
	out := sb.String()

	for _, want := range []string{
		"✗ 1 need attention · 1 warning(s) · 1 ok",
		"Needs attention",
		"blog: missing required secret",
		"→ herald secret set blog/db_password",
		"Warnings",
		"old-wiki: orphan",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
	// Ordering: verdict, then Needs attention before Warnings.
	if strings.Index(out, "Needs attention") > strings.Index(out, "Warnings") {
		t.Error("Needs attention should precede Warnings")
	}
}

func TestRenderInventoryAndLastPass(t *testing.T) {
	di := &Diagnosis{
		Server: "srv1",
		Stacks: []StackInventory{
			{Name: "blog", Source: "me/blog@main", Domain: "blog.example.com", DeployRef: "main@abc123"},
			{Name: "nc", Source: "path:apps/nc", Domain: "nc.example.com", AutoDeploy: true,
				Secrets: []string{"nc/db → env:DB_PASS"}, Update: "./update.sh"},
		},
		Webhooks: []WebhookInventory{{Repo: "me/blog", State: "active"}, {Repo: "me/server", State: "active"}},
		LastPass: &maintenance.Report{
			Webhooks: maintenance.WebhookResult{Synced: 2, Created: 1},
		},
	}
	var sb strings.Builder
	di.Render(&sb)
	out := sb.String()

	for _, want := range []string{
		"Inventory",
		"me/blog@main",
		"webhook: active", // blog's webhook nested in its block
		"path:apps/nc",
		"secret: nc/db → env:DB_PASS",
		"update: ./update.sh",
		"Server repo", // me/server has no stack, listed separately
		"me/server",
		"Last maintenance pass",
		"2 synced, 1 created",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n---\n%s", want, out)
		}
	}
}
