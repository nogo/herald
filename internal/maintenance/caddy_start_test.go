package maintenance_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/caddy"
	"github.com/nogo/herald/internal/config"
	"github.com/nogo/herald/internal/secrets"
)

func TestStartDNSCredential(t *testing.T) {
	for _, hasToken := range []bool{false, true} {
		t.Run(fmt.Sprint(hasToken), func(t *testing.T) {
			dir := t.TempDir()
			store := secrets.NewStore(dir)
			if err := store.Init(); err != nil {
				t.Fatal(err)
			}
			const token = "private-hetzner-token"
			if hasToken {
				if err := store.Set(caddy.HetznerTokenKey, token); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/sh
case "$1" in
 network) if [ "$3" = bridge ]; then echo 172.17.0.1; fi ;;
 compose) printf '%s' "$HERALD_HETZNER_TOKEN" > "$DNS_TEST_CAPTURE" ;;
 ps) echo container ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			capture := filepath.Join(dir, "capture")
			t.Setenv("DNS_TEST_CAPTURE", capture)
			m := &caddy.CaddyManager{Config: &config.Config{Server: config.Server{ServicesDir: dir, TLS: &config.TLSConfig{DNS: "hetzner", Wildcard: "*.example.com"}}}, Secrets: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), HeraldPort: 9483}
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(dir, "caddy", "compose.yml"))
			if err != nil {
				t.Fatal(err)
			}
			env, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(content), token) {
				t.Fatal("token written into compose")
			}
			if hasToken {
				if string(env) != token || !strings.Contains(string(content), "build: .") {
					t.Fatal("DNS credential not delivered")
				}
				built, err := os.ReadFile(filepath.Join(dir, "caddy", "Dockerfile"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(built), "github.com/caddy-dns/hetzner/v2@v2.0.0-preview-3") || strings.Contains(string(built), token) {
					t.Fatal("wrong Dockerfile")
				}
				if m.DNSWarning() != "" {
					t.Fatal("unexpected DNS warning")
				}
				m.Config.Server.TLS = nil
				if err := m.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				env, err = os.ReadFile(capture)
				if err != nil || len(env) != 0 {
					t.Fatal("token passed without server.tls")
				}
			} else {
				if string(env) != "" || strings.Contains(string(content), "acme_dns") || strings.Contains(string(content), "caddy_1:") || !strings.Contains(string(content), "lucaslorentz/caddy-docker-proxy:2.9") {
					t.Fatal("missing token did not preserve stock setup")
				}
				if !strings.Contains(m.DNSWarning(), caddy.HetznerTokenKey) {
					t.Fatal("missing warning")
				}
			}
			if err := m.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			env, err = os.ReadFile(capture)
			if err != nil || string(env) != "unused" {
				t.Fatal("down requires a credential")
			}
		})
	}
}
