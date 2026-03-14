package compose

import (
	"os"
	"path/filepath"
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

func TestDetectServiceInfo(t *testing.T) {
	t.Run("service named app", func(t *testing.T) {
		content := `
services:
  app:
    expose:
      - "3000"
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "myapp", "3000")
		if name != "app" {
			t.Errorf("service name = %q, want %q", name, "app")
		}
		if port != "3000" {
			t.Errorf("port = %q, want %q", port, "3000")
		}
	})

	t.Run("service matches app name", func(t *testing.T) {
		content := `
services:
  budget:
    ports:
      - "8080:8080"
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "budget", "3000")
		if name != "budget" {
			t.Errorf("service name = %q, want %q", name, "budget")
		}
		if port != "8080" {
			t.Errorf("port = %q, want %q", port, "8080")
		}
	})

	t.Run("first service fallback", func(t *testing.T) {
		content := `
services:
  web:
    expose:
      - 4000
  db:
    image: postgres
`
		name, port := writeComposeAndDetect(t, content, "other", "3000")
		if name != "db" && name != "web" {
			t.Errorf("service name = %q, want one of [db web]", name)
		}
		_ = port
	})

	t.Run("port from long form", func(t *testing.T) {
		content := `
services:
  app:
    ports:
      - target: 5000
        published: 80
`
		_, port := writeComposeAndDetect(t, content, "app", "3000")
		if port != "5000" {
			t.Errorf("port = %q, want %q", port, "5000")
		}
	})

	t.Run("no port defaults to defaultPort", func(t *testing.T) {
		content := `
services:
  app:
    image: myapp
`
		_, port := writeComposeAndDetect(t, content, "app", "3000")
		if port != "3000" {
			t.Errorf("port = %q, want %q", port, "3000")
		}
	})

	t.Run("stack default port 80", func(t *testing.T) {
		content := `
services:
  app:
    image: myapp
`
		_, port := writeComposeAndDetect(t, content, "app", "80")
		if port != "80" {
			t.Errorf("port = %q, want %q", port, "80")
		}
	})
}

func writeComposeAndDetect(t *testing.T, content, appName, defaultPort string) (name, port string) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	name, port, err := DetectServiceInfo(f, appName, defaultPort)
	if err != nil {
		t.Fatalf("DetectServiceInfo: %v", err)
	}
	return name, port
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
