package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortFromAny(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"3000", "3000"},
		{"3000:3000", "3000"},
		{"0.0.0.0:80:3000", "3000"},
		{3000, "3000"},
		{8080, "8080"},
		{map[string]any{"target": 3000, "published": 3000}, "3000"},
		{map[string]any{"target": "8080"}, "8080"},
		{nil, ""},
	}
	for _, tc := range tests {
		got := portFromAny(tc.in)
		if got != tc.want {
			t.Errorf("portFromAny(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSelectRoute(t *testing.T) {
	const webAndDB = "services:\n  db:\n    image: postgres\n  web:\n    expose:\n      - 4000\n"
	tests := []struct {
		name, compose, service, port string
		wantService, wantPort        string
		wantErr                      string // substring; empty means success
	}{
		{name: "a service named app is not special", compose: "services:\n  app:\n    expose: [\"3000\"]\n  db:\n    image: postgres\n", wantErr: "set `service:`"},
		{name: "a service named after the stack is not special", compose: "services:\n  budget:\n    ports: [\"8080:8080\"]\n  db:\n    image: postgres\n", wantErr: "set `service:`"},
		{name: "sole service", compose: "services:\n  web:\n    expose: [\"3000\"]\n", wantService: "web", wantPort: "3000"},
		{name: "no declared port is not guessed", compose: "services:\n  web:\n    image: x\n", wantErr: "set `port:`"},
		{name: "no declared port resolved by explicit port", compose: "services:\n  web:\n    image: x\n", port: "80", wantService: "web", wantPort: "80"},
		{name: "web plus database is ambiguous", compose: webAndDB, wantErr: "set `service:`"},
		{name: "explicit service", compose: webAndDB, service: "web", wantService: "web", wantPort: "4000"},
		{name: "missing explicit service", compose: webAndDB, service: "api", wantErr: `service "api" not found`},
		{name: "explicit port", compose: webAndDB, service: "web", port: "9000", wantService: "web", wantPort: "9000"},
		{name: "non-numeric port", compose: webAndDB, service: "web", port: "http", wantErr: `"http"`},
		{name: "port out of range", compose: webAndDB, service: "web", port: "70000", wantErr: `"70000"`},
		{name: "ambiguous ports", compose: "services:\n  app:\n    expose: [\"3000\"]\n    ports: [\"80:8080\"]\n", wantErr: "set `port:`"},
		{name: "ambiguous ports resolved by explicit port", compose: "services:\n  app:\n    expose: [\"3000\"]\n    ports: [\"80:8080\"]\n", port: "8080", wantService: "app", wantPort: "8080"},
		{name: "same target in expose and ports", compose: "services:\n  app:\n    expose: [\"3000\"]\n    ports: [\"3000:3000\"]\n", wantService: "app", wantPort: "3000"},
		{name: "long form", compose: "services:\n  app:\n    ports:\n      - target: 5000\n        published: 80\n", wantService: "app", wantPort: "5000"},
		{name: "protocol suffix", compose: "services:\n  app:\n    ports: [\"8080:3000/tcp\"]\n", wantService: "app", wantPort: "3000"},
		{name: "no services", compose: "services: {}\n", wantErr: "no services"},
		{name: "malformed yaml", compose: "services: [unclosed", wantErr: "compose.yml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "compose.yml")
			if err := os.WriteFile(f, []byte(tc.compose), 0644); err != nil {
				t.Fatal(err)
			}
			got, err := SelectRoute(f, tc.service, tc.port)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Service != tc.wantService || got.Port != tc.wantPort {
				t.Errorf("route = %s:%s, want %s:%s", got.Service, got.Port, tc.wantService, tc.wantPort)
			}
		})
	}

	t.Run("unreadable file names it", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "gone.yml")
		_, err := SelectRoute(f, "", "")
		if err == nil || !strings.Contains(err.Error(), f) {
			t.Fatalf("err = %v, want it to name %s", err, f)
		}
	})
}

func TestDeepMerge(t *testing.T) {
	t.Run("overlay wins on conflict", func(t *testing.T) {
		base := map[string]any{"a": "base", "b": "keep"}
		overlay := map[string]any{"a": "overlay"}
		got := DeepMerge(base, overlay)
		if got["a"] != "overlay" {
			t.Errorf("a = %v, want overlay", got["a"])
		}
		if got["b"] != "keep" {
			t.Errorf("b = %v, want keep", got["b"])
		}
	})

	t.Run("nested maps merged", func(t *testing.T) {
		base := map[string]any{
			"services": map[string]any{
				"app": map[string]any{"image": "old"},
			},
		}
		overlay := map[string]any{
			"services": map[string]any{
				"app": map[string]any{"restart": "always"},
			},
		}
		got := DeepMerge(base, overlay)
		svc := got["services"].(map[string]any)["app"].(map[string]any)
		if svc["image"] != "old" {
			t.Errorf("image = %v, want old", svc["image"])
		}
		if svc["restart"] != "always" {
			t.Errorf("restart = %v, want always", svc["restart"])
		}
	})

	t.Run("overlay adds new keys", func(t *testing.T) {
		base := map[string]any{"a": 1}
		overlay := map[string]any{"b": 2}
		got := DeepMerge(base, overlay)
		if got["a"] != 1 || got["b"] != 2 {
			t.Errorf("got %v, want {a:1, b:2}", got)
		}
	})
}

func TestDeepMergeYAML(t *testing.T) {
	t.Run("preserves override tag", func(t *testing.T) {
		base := `
services:
  budget-app:
    env_file: !override
      - /opt/deploy/apps/budget/.env
    labels:
      caddy: budget.example.com
    networks:
      - caddy
networks:
  caddy:
    external: true
`
		overlay := `
services:
  budget-app:
    env_file: !override
      - /opt/deploy/apps/budget/.env
  budget-migrate:
    env_file: !override
      - /opt/deploy/apps/budget/.env
volumes:
  budget-data:
    driver: local
`
		merged, err := DeepMergeYAML([]byte(base), []byte(overlay))
		if err != nil {
			t.Fatalf("DeepMergeYAML: %v", err)
		}
		result := string(merged)

		// The !override tag must be preserved in the merged output.
		if !strings.Contains(result, "!override") {
			t.Errorf("merged YAML lost !override tag:\n%s", result)
		}

		// Both services should be present.
		if !strings.Contains(result, "budget-app") {
			t.Errorf("merged YAML missing budget-app:\n%s", result)
		}
		if !strings.Contains(result, "budget-migrate") {
			t.Errorf("merged YAML missing budget-migrate:\n%s", result)
		}

		// Volumes from overlay should be present.
		if !strings.Contains(result, "budget-data") {
			t.Errorf("merged YAML missing budget-data volume:\n%s", result)
		}

		// Labels from base should survive the merge.
		if !strings.Contains(result, "budget.example.com") {
			t.Errorf("merged YAML lost caddy label from base:\n%s", result)
		}

		// Networks from base should survive.
		if !strings.Contains(result, "caddy") {
			t.Errorf("merged YAML lost caddy network:\n%s", result)
		}
	})

	t.Run("overlay replaces non-map values", func(t *testing.T) {
		base := `
services:
  app:
    image: old:v1
    labels:
      caddy: old.example.com
`
		overlay := `
services:
  app:
    image: new:v2
`
		merged, err := DeepMergeYAML([]byte(base), []byte(overlay))
		if err != nil {
			t.Fatalf("DeepMergeYAML: %v", err)
		}
		result := string(merged)

		if !strings.Contains(result, "new:v2") {
			t.Errorf("overlay image not applied:\n%s", result)
		}
		// Labels from base should survive (maps merge).
		if !strings.Contains(result, "old.example.com") {
			t.Errorf("base labels lost:\n%s", result)
		}
	})
}

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	envVars := map[string]string{
		"DB_URL":  "postgres://localhost/db",
		"API_KEY": "secret123",
	}

	if err := WriteEnvFile(root, envVars); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Keys should be sorted: API_KEY before DB_URL
	if content != "API_KEY=secret123\nDB_URL=postgres://localhost/db\n" {
		t.Errorf("unexpected .env content:\n%s", content)
	}
}

func TestWriteEnvFileEmpty(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := WriteEnvFile(root, map[string]string{}); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %q", string(data))
	}
}
