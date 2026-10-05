package deployer

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/config"
	"gopkg.in/yaml.v3"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(100)}))
}

func TestLoadConfigFile(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.env")
		os.WriteFile(path, []byte("FOO=bar\nBAZ=qux\n"), 0600)
		got, err := LoadConfigFile(path, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if got["FOO"] != "bar" || got["BAZ"] != "qux" {
			t.Errorf("unexpected result: %v", got)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 keys, got %d", len(got))
		}
	})

	t.Run("comments", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.env")
		os.WriteFile(path, []byte("# this is a comment\nFOO=bar\n"), 0600)
		got, err := LoadConfigFile(path, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if got["FOO"] != "bar" || len(got) != 1 {
			t.Errorf("expected only FOO=bar, got %v", got)
		}
	})

	t.Run("blank lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.env")
		os.WriteFile(path, []byte("\nFOO=bar\n\nBAZ=qux\n"), 0600)
		got, err := LoadConfigFile(path, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 keys, got %d: %v", len(got), got)
		}
	})

	t.Run("no equals line", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.env")
		os.WriteFile(path, []byte("NOEQUALS\nFOO=bar\n"), 0600)
		got, err := LoadConfigFile(path, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got["NOEQUALS"]; ok {
			t.Error("line without = should be skipped")
		}
		if got["FOO"] != "bar" {
			t.Errorf("expected FOO=bar, got %v", got)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadConfigFile("/nonexistent/path/config.env", discardLogger())
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

func TestBuildEnvMap(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		envVars := map[string]string{"KEY": "value"}
		got, err := BuildEnvMap("", "/any", envVars, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if got["KEY"] != "value" || len(got) != 1 {
			t.Errorf("expected pass-through, got %v", got)
		}
	})

	t.Run("config and secrets merge", func(t *testing.T) {
		dir := t.TempDir()
		configFile := "app.env"
		os.WriteFile(filepath.Join(dir, configFile), []byte("BASE=from_config\nFOO=from_config\n"), 0600)
		envVars := map[string]string{"EXTRA": "from_secret"}
		got, err := BuildEnvMap(configFile, dir, envVars, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if got["BASE"] != "from_config" {
			t.Errorf("expected BASE=from_config, got %q", got["BASE"])
		}
		if got["FOO"] != "from_config" {
			t.Errorf("expected FOO=from_config, got %q", got["FOO"])
		}
		if got["EXTRA"] != "from_secret" {
			t.Errorf("expected EXTRA=from_secret, got %q", got["EXTRA"])
		}
	})

	t.Run("secret overrides config key", func(t *testing.T) {
		dir := t.TempDir()
		configFile := "app.env"
		os.WriteFile(filepath.Join(dir, configFile), []byte("KEY=from_config\n"), 0600)
		envVars := map[string]string{"KEY": "from_secret"}
		got, err := BuildEnvMap(configFile, dir, envVars, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if got["KEY"] != "from_secret" {
			t.Errorf("expected secret to win, got %q", got["KEY"])
		}
	})
}

func TestGenerateOverride(t *testing.T) {
	t.Run("basic with caddy labels", func(t *testing.T) {
		dir := t.TempDir()
		composeFile := filepath.Join(dir, "compose.yml")
		os.WriteFile(composeFile, []byte("services:\n  app:\n    expose:\n      - \"3000\"\n"), 0644)
		params := OverrideParams{
			DeployDir:   dir,
			StackName:   "myapp",
			Domain:      "myapp.example.com",
			ComposeFile: composeFile,
			DefaultPort: "3000",
			InternalNet: "herald-myapp-internal",
		}
		data, err := GenerateOverride(params)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		if !strings.Contains(s, "myapp.example.com") {
			t.Errorf("expected domain in output:\n%s", s)
		}
		if !strings.Contains(s, "caddy") {
			t.Errorf("expected caddy label in output:\n%s", s)
		}
		if !strings.Contains(s, "herald-myapp-internal") {
			t.Errorf("expected internal network in output:\n%s", s)
		}
	})

	t.Run("with docker secrets", func(t *testing.T) {
		dir := t.TempDir()
		params := OverrideParams{
			DeployDir:     dir,
			StackName:     "myapp",
			Domain:        "myapp.example.com",
			ComposeFile:   writeTestCompose(t, dir, "services:\n  app:\n    image: x\n"),
			DockerSecrets: map[string]string{"DB_PASSWORD": "secret123"},
			DefaultPort:   "3000",
			InternalNet:   "herald-myapp-internal",
		}
		data, err := GenerateOverride(params)
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		if !strings.Contains(s, "DB_PASSWORD") {
			t.Errorf("expected secret name in output:\n%s", s)
		}
		if !strings.Contains(s, "secrets:") {
			t.Errorf("expected secrets section in output:\n%s", s)
		}
		if !strings.Contains(s, filepath.Join(dir, "secrets", "DB_PASSWORD")) {
			t.Errorf("expected secret file path in output:\n%s", s)
		}
	})

	t.Run("inline override preserves YAML tags", func(t *testing.T) {
		dir := t.TempDir()
		params := OverrideParams{
			DeployDir:      dir,
			StackName:      "myapp",
			Domain:         "myapp.example.com",
			ComposeFile:    writeTestCompose(t, dir, "services:\n  app:\n    image: x\n"),
			DefaultPort:    "3000",
			InternalNet:    "herald-myapp-internal",
			InlineOverride: "services:\n  app:\n    env_file: !override\n      - custom.env\n",
		}
		data, err := GenerateOverride(params)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "!override") {
			t.Errorf("expected !override tag preserved in output:\n%s", data)
		}
	})

	t.Run("with env file paths", func(t *testing.T) {
		dir := t.TempDir()
		envPath := filepath.Join(dir, ".env")
		params := OverrideParams{
			DeployDir:    dir,
			StackName:    "myapp",
			Domain:       "myapp.example.com",
			ComposeFile:  writeTestCompose(t, dir, "services:\n  app:\n    image: x\n"),
			EnvFilePaths: []string{envPath},
			DefaultPort:  "3000",
			InternalNet:  "herald-myapp-internal",
		}
		data, err := GenerateOverride(params)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), envPath) {
			t.Errorf("expected env file path in output:\n%s", data)
		}
	})
}

func TestWriteDockerSecrets(t *testing.T) {
	t.Run("writes files", func(t *testing.T) {
		dir := t.TempDir()
		secretsDir := filepath.Join(dir, "secrets")
		secrets := map[string]string{
			"DB_PASSWORD": "mypassword",
			"API_KEY":     "myapikey",
		}
		if err := WriteDockerSecrets(secretsDir, secrets); err != nil {
			t.Fatal(err)
		}
		for name, val := range secrets {
			data, err := os.ReadFile(filepath.Join(secretsDir, name))
			if err != nil {
				t.Errorf("secret file %s not found: %v", name, err)
				continue
			}
			if string(data) != val {
				t.Errorf("secret %s: expected %q, got %q", name, val, string(data))
			}
		}
	})

	t.Run("0600 permissions", func(t *testing.T) {
		dir := t.TempDir()
		secretsDir := filepath.Join(dir, "secrets")
		if err := WriteDockerSecrets(secretsDir, map[string]string{"mysecret": "val"}); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(filepath.Join(secretsDir, "mysecret"))
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected 0600, got %04o", perm)
		}
	})
}

func TestConfigDrifted(t *testing.T) {
	stack := config.Stack{
		Repo:   "acme/app",
		Branch: "main",
		Domain: "app.example.com",
	}

	dir := t.TempDir()

	// No stamp at all: a stack deployed before Herald recorded fingerprints must
	// not be reported as drifted, or an upgrade flags every stack at once.
	if (Instance{Dir: dir}).ConfigDrifted(stack) {
		t.Error("ConfigDrifted = true with no stamp, want false")
	}

	stamp := func(s config.Stack) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "deployed_config"), []byte(s.Hash()), 0644); err != nil {
			t.Fatal(err)
		}
	}

	stamp(stack)
	if (Instance{Dir: dir}).ConfigDrifted(stack) {
		t.Error("ConfigDrifted = true for an unchanged stack, want false")
	}

	// The case that silently broke TLS: domain edited, source untouched.
	moved := stack
	moved.Domain = "new.example.com"
	if !(Instance{Dir: dir}).ConfigDrifted(moved) {
		t.Error("ConfigDrifted = false after a domain change, want true")
	}

	// Non-routing fields count too — any config.yml edit needs a redeploy to apply.
	renamed := stack
	renamed.Branch = "release"
	if !(Instance{Dir: dir}).ConfigDrifted(renamed) {
		t.Error("ConfigDrifted = false after a branch change, want true")
	}

	stamp(moved)
	if (Instance{Dir: dir}).ConfigDrifted(moved) {
		t.Error("ConfigDrifted = true right after redeploying the changed stack, want false")
	}
}

func TestStackHashStable(t *testing.T) {
	s := config.Stack{Repo: "acme/app", Domain: "app.example.com", Secrets: []config.SecretRef{
		{Key: "db/password", Type: "env", Target: "DB_PASSWORD"},
	}}
	first, second := s.Hash(), s.Hash()
	if first == "" {
		t.Fatal("Hash() = empty")
	}
	if first != second {
		t.Error("Hash() is not stable across calls")
	}
	other := s
	other.Domain = "other.example.com"
	if s.Hash() == other.Hash() {
		t.Error("Hash() collides across different domains")
	}
}

func writeTestCompose(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Production and previews both reach GenerateOverride through Up, so the
// routing decision is tested here once for the shared path.
func TestGenerateOverrideRouting(t *testing.T) {
	const webAndDB = "services:\n  db:\n    image: postgres\n  web:\n    ports:\n      - \"8080:8080\"\n"
	gen := func(t *testing.T, compose, service, port string) ([]byte, error) {
		dir := t.TempDir()
		return GenerateOverride(OverrideParams{
			DeployDir:   dir,
			StackName:   "shop",
			Domain:      "shop.example.com",
			ComposeFile: writeTestCompose(t, dir, compose),
			Service:     service,
			Port:        port,
			DefaultPort: "3000",
			InternalNet: "herald-shop-internal",
		})
	}

	t.Run("web plus database without service fails", func(t *testing.T) {
		_, err := gen(t, webAndDB, "", "")
		if err == nil || !strings.Contains(err.Error(), "service:") {
			t.Fatalf("want error asking for a service, got %v", err)
		}
	})

	t.Run("explicit service routes to it and isolates the rest", func(t *testing.T) {
		data, err := gen(t, webAndDB, "web", "")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Services map[string]struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Services["web"].Labels["caddy"] != "shop.example.com" ||
			got.Services["web"].Labels["caddy.reverse_proxy"] != "{{upstreams 8080}}" {
			t.Errorf("web not routed on 8080:\n%s", data)
		}
		if len(got.Services["db"].Labels) != 0 {
			t.Errorf("db must not carry caddy labels:\n%s", data)
		}
	})

	t.Run("explicit port wins", func(t *testing.T) {
		data, err := gen(t, webAndDB, "web", "9000")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "{{upstreams 9000}}") {
			t.Errorf("want port 9000:\n%s", data)
		}
	})

	t.Run("malformed compose names the file", func(t *testing.T) {
		_, err := gen(t, "services: [unclosed", "", "")
		if err == nil || !strings.Contains(err.Error(), "compose.yml") {
			t.Fatalf("want error naming compose.yml, got %v", err)
		}
	})

	t.Run("missing compose names the file", func(t *testing.T) {
		_, err := GenerateOverride(OverrideParams{
			ComposeFile: filepath.Join(t.TempDir(), "missing.yml"),
			DefaultPort: "3000",
		})
		if err == nil || !strings.Contains(err.Error(), "missing.yml") {
			t.Fatalf("want error naming missing.yml, got %v", err)
		}
	})
}

func TestGenerateOverrideUpstream(t *testing.T) {
	const hostNet = "services:\n  homeassistant:\n    image: ha\n    network_mode: host\n"
	gen := func(t *testing.T, upstream, inline string) ([]byte, error) {
		dir := t.TempDir()
		return GenerateOverride(OverrideParams{
			DeployDir:      dir,
			StackName:      "homeassistant",
			Domain:         "ha.example.com",
			ComposeFile:    writeTestCompose(t, dir, hostNet),
			DefaultPort:    "80",
			InternalNet:    "herald-homeassistant-internal",
			Upstream:       upstream,
			GatewayIP:      "172.17.0.1",
			InlineOverride: inline,
		})
	}
	parse := func(t *testing.T, data []byte) (map[string]string, []string) {
		t.Helper()
		var got struct {
			Services map[string]struct {
				Labels   map[string]string `yaml:"labels"`
				Networks []string          `yaml:"networks"`
			} `yaml:"services"`
			Networks map[string]any `yaml:"networks"`
		}
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Networks["caddy"]; ok {
			t.Errorf("caddy network must not be declared:\n%s", data)
		}
		return got.Services["homeassistant"].Labels, got.Services["homeassistant"].Networks
	}

	t.Run("host upstream resolves to the gateway", func(t *testing.T) {
		data, err := gen(t, "host:8123", "")
		if err != nil {
			t.Fatal(err)
		}
		labels, networks := parse(t, data)
		if labels["caddy"] != "ha.example.com" || labels["caddy.reverse_proxy"] != "172.17.0.1:8123" {
			t.Errorf("labels = %v", labels)
		}
		if len(networks) != 0 {
			t.Errorf("networks = %v, want none", networks)
		}
	})

	t.Run("ip upstream is used as given", func(t *testing.T) {
		data, err := gen(t, "192.168.42.10:8043", "")
		if err != nil {
			t.Fatal(err)
		}
		labels, _ := parse(t, data)
		if labels["caddy.reverse_proxy"] != "192.168.42.10:8043" {
			t.Errorf("labels = %v", labels)
		}
	})

	t.Run("override labels merge on top", func(t *testing.T) {
		inline := "services:\n  homeassistant:\n    labels:\n      caddy.reverse_proxy.transport: http\n      caddy.reverse_proxy.transport.tls_insecure_skip_verify: \"\"\n"
		data, err := gen(t, "host:8043", inline)
		if err != nil {
			t.Fatal(err)
		}
		labels, _ := parse(t, data)
		if labels["caddy.reverse_proxy"] != "172.17.0.1:8043" || labels["caddy.reverse_proxy.transport"] != "http" {
			t.Errorf("labels = %v", labels)
		}
		if _, ok := labels["caddy.reverse_proxy.transport.tls_insecure_skip_verify"]; !ok {
			t.Errorf("merged label missing: %v", labels)
		}
	})

	t.Run("invalid upstream names the stack", func(t *testing.T) {
		for _, bad := range []string{"8123", "host", "host:0", "host:http", "example.com:80", "localhost:80"} {
			_, err := gen(t, bad, "")
			if err == nil || !strings.Contains(err.Error(), `stack "homeassistant"`) {
				t.Errorf("upstream %q: want error naming the stack, got %v", bad, err)
			}
		}
	})

	t.Run("without upstream the override is unchanged", func(t *testing.T) {
		dir := t.TempDir()
		data, err := GenerateOverride(OverrideParams{
			DeployDir:   dir,
			StackName:   "myapp",
			Domain:      "myapp.example.com",
			ComposeFile: writeTestCompose(t, dir, "services:\n  app:\n    expose:\n      - \"3000\"\n"),
			DefaultPort: "3000",
			InternalNet: "herald-myapp-internal",
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `services:
    app:
        labels:
            caddy: myapp.example.com
            caddy.reverse_proxy: '{{upstreams 3000}}'
        networks: !override
            - caddy
            - herald-myapp-internal
networks:
    caddy:
        external: true
    herald-myapp-internal: {}
`
		if string(data) != want {
			t.Errorf("override changed:\n%s", data)
		}
	})
}
