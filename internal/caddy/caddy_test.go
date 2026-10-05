package caddy

import (
	"bytes"
	"github.com/nogo/herald/internal/config"
	"os"
	"strings"
	"testing"
	"text/template"
)

func TestGenerateComposeContent(t *testing.T) {
	content := generateComposeContent("admin@example.com", "", "deploy.example.com", 8080, nil)

	checks := []string{
		"lucaslorentz/caddy-docker-proxy:2.9",
		"herald-caddy",
		`caddy.email: "admin@example.com"`,
		`caddy_0: "deploy.example.com"`,
		"external: true",
		"caddy_data:",
		"caddy_config:",
		"CADDY_INGRESS_NETWORKS=caddy",
		"/var/run/docker.sock:/var/run/docker.sock:ro",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("compose content missing %q", want)
		}
	}

	// Must contain the gateway IP + port for reverse proxy
	if !strings.Contains(content, ":8080") {
		t.Errorf("compose content missing port 8080")
	}

	// Must NOT contain herald-proxy sidecar (removed)
	if strings.Contains(content, "herald-proxy-label") {
		t.Errorf("compose should not contain the old herald-proxy sidecar")
	}

	// No acme_ca given: Caddy keeps its default issuer chain.
	if strings.Contains(content, "acme_ca") {
		t.Errorf("compose should omit acme_ca when unset, got:\n%s", content)
	}
}

func TestGenerateComposeContentAcmeCA(t *testing.T) {
	const staging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	content := generateComposeContent("admin@example.com", staging, "deploy.example.com", 8080, nil)
	if !strings.Contains(content, `caddy.acme_ca: "`+staging+`"`) {
		t.Errorf("compose content missing acme_ca label, got:\n%s", content)
	}
}

func TestGenerateComposeContentPort(t *testing.T) {
	content := generateComposeContent("x@x.com", "", "x.com", 9483, nil)
	if !strings.Contains(content, ":9483") {
		t.Errorf("expected port 9483 in compose content")
	}
}

func TestGetDockerGatewayIPFallback(t *testing.T) {
	// On CI/test environments without Docker, should return the fallback
	ip := DockerGatewayIP()
	if ip == "" {
		t.Error("DockerGatewayIP returned empty string")
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
			name:      "direct reverse proxy",
			container: "herald-caddy",
			labels: map[string]string{
				"caddy_0":               "deploy.example.com",
				"caddy_0.reverse_proxy": "172.17.0.1:9483",
			},
			want: "herald-caddy",
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

func TestStockComposeUnchanged(t *testing.T) {
	original, err := os.ReadFile("testdata/stock-compose.yml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("stock").Parse(string(original)))
	var want bytes.Buffer
	err = tmpl.Execute(&want, composeData{AcmeEmail: "ops@example.com", AcmeCA: "https://ca.example.com", DeployDomain: "deploy.example.com", GatewayIP: DockerGatewayIP(), HeraldPort: 9483})
	if err != nil {
		t.Fatal(err)
	}
	got := generateComposeContent("ops@example.com", "https://ca.example.com", "deploy.example.com", 9483, nil)
	if got != want.String() {
		t.Fatalf("stock compose changed:\n%s", got)
	}
}

func TestGenerateDNSCompose(t *testing.T) {
	for _, wildcard := range []string{"", "*.example.com"} {
		t.Run(wildcard, func(t *testing.T) {
			content := generateComposeContent("ops@example.com", "", "deploy.example.com", 9483, &config.TLSConfig{DNS: "hetzner", Wildcard: wildcard})
			for _, want := range []string{"build: .", "HETZNER_API_TOKEN=${HERALD_HETZNER_TOKEN:?}", `caddy.acme_dns: "hetzner {env.HETZNER_API_TOKEN}"`} {
				if !strings.Contains(content, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Contains(content, "lucaslorentz/caddy-docker-proxy:2.9") {
				t.Fatal("DNS compose uses stock image")
			}
			if wildcard != "" {
				if !strings.Contains(content, `caddy_1: "*.example.com"`) {
					t.Fatal("wildcard configuration missing")
				}
				// Caddy 2.11 rejects auto_https prefer_wildcard and drops every global option with it.
				if strings.Contains(content, "caddy.auto_https") {
					t.Fatal("compose sets auto_https, which Caddy 2.11 rejects")
				}
			} else if strings.Contains(content, "caddy_1:") {
				t.Fatal("unexpected wildcard")
			}
		})
	}
}

func TestComposeOmitsHeraldSiteWithoutDeployDomain(t *testing.T) {
	got := generateComposeContent("ops@example.com", "", "", 9483, nil)
	if strings.Contains(got, "caddy_0") {
		t.Fatalf("compose has a Herald site without deploy_domain:\n%s", got)
	}
}
