package caddy

import (
	"strings"
	"testing"
)

func TestGenerateComposeContent(t *testing.T) {
	content := generateComposeContent("admin@example.com", "deploy.example.com", 8080)

	checks := []string{
		"lucaslorentz/caddy-docker-proxy:2.9",
		"herald-caddy",
		`caddy.email: "admin@example.com"`,
		`caddy: "deploy.example.com"`,
		"host.docker.internal:8080",
		"external: true",
		"caddy_data:",
		"caddy_config:",
		"CADDY_INGRESS_NETWORKS=caddy",
		"herald-proxy-label",
		"/var/run/docker.sock:/var/run/docker.sock:ro",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("compose content missing %q", want)
		}
	}
}

func TestGenerateComposeContentPort(t *testing.T) {
	content := generateComposeContent("x@x.com", "x.com", 9090)
	if !strings.Contains(content, "host.docker.internal:9090") {
		t.Errorf("expected port 9090 in compose content")
	}
}

func TestParseCaddyUpstream(t *testing.T) {
	tests := []struct {
		name      string
		container string
		labels    map[string]string
		want      string
	}{
		{
			name:      "herald proxy",
			container: "herald-proxy-label",
			labels: map[string]string{
				"caddy":                   "deploy.example.com",
				"caddy.reverse_proxy":     "{{upstreams}}",
				"caddy.reverse_proxy.to":  "host.docker.internal:8080",
			},
			want: "host:8080 (herald)",
		},
		{
			name:      "app container",
			container: "herald-budget-app",
			labels: map[string]string{
				"caddy":               "budget.example.com",
				"caddy.reverse_proxy": "{{upstreams 3000}}",
			},
			want: "herald-budget-app:3000",
		},
		{
			name:      "no upstream info",
			container: "my-app",
			labels: map[string]string{
				"caddy": "app.example.com",
			},
			want: "my-app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCaddyUpstream(tt.container, tt.labels)
			if got != tt.want {
				t.Errorf("parseCaddyUpstream() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatUptimeInvalid(t *testing.T) {
	if got := formatUptime("not-a-time"); got != "unknown" {
		t.Errorf("formatUptime(invalid) = %q, want %q", got, "unknown")
	}
}
