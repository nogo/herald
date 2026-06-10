package doctor

import (
	"strings"
	"testing"

	"github.com/nogo/herald/internal/maintenance"
)

func TestRenderGroupsBySeverity(t *testing.T) {
	di := &Diagnosis{
		Server: "srv1",
		Checks: []Check{
			{Name: "docker accessible", Severity: SeverityOK},
			{Name: "blog: missing required secret", Severity: SeverityAttention,
				Detail: "blog/db_password", Fix: "herald secret set blog/db_password"},
			{Name: "old-wiki: orphan", Severity: SeverityWarning,
				Detail: "running but not in config", Fix: "herald down old-wiki"},
		},
	}

	var sb strings.Builder
	di.Render(&sb)
	out := sb.String()

	for _, want := range []string{
		"Herald doctor — srv1",
		"OK:",
		"docker accessible",
		"Needs attention:",
		"blog: missing required secret",
		"fix: herald secret set blog/db_password",
		"Warnings:",
		"old-wiki: orphan",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q\n---\n%s", want, out)
		}
	}

	// Ordering: OK before Needs attention before Warnings.
	okIdx := strings.Index(out, "OK:")
	attnIdx := strings.Index(out, "Needs attention:")
	warnIdx := strings.Index(out, "Warnings:")
	if !(okIdx < attnIdx && attnIdx < warnIdx) {
		t.Errorf("sections out of order: ok=%d attn=%d warn=%d", okIdx, attnIdx, warnIdx)
	}
}

func TestRenderNoProblems(t *testing.T) {
	di := &Diagnosis{
		Server: "srv1",
		Checks: []Check{{Name: "docker accessible", Severity: SeverityOK}},
	}
	var sb strings.Builder
	di.Render(&sb)
	if !strings.Contains(sb.String(), "No problems detected.") {
		t.Errorf("expected clean bill of health, got:\n%s", sb.String())
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
		Webhooks: []WebhookInventory{{Repo: "me/blog", State: "active"}},
		LastPass: &maintenance.Report{
			Webhooks: maintenance.WebhookResult{Synced: 2, Created: 1},
		},
	}
	var sb strings.Builder
	di.Render(&sb)
	out := sb.String()

	for _, want := range []string{
		"Inventory:",
		"me/blog@main",
		"path:apps/nc",
		"secret: nc/db → env:DB_PASS",
		"update: ./update.sh",
		"Webhooks:",
		"me/blog",
		"Last maintenance pass:",
		"2 synced, 1 created",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q\n---\n%s", want, out)
		}
	}
}
