package deployer

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			ComposeFile:   filepath.Join(dir, "nonexistent.yml"),
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
			ComposeFile:    filepath.Join(dir, "nonexistent.yml"),
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
			ComposeFile:  filepath.Join(dir, "nonexistent.yml"),
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

func TestWriteEnvFile(t *testing.T) {
	t.Run("sorted output", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "output.env")
		envMap := map[string]string{
			"ZZZ": "last",
			"AAA": "first",
			"MMM": "middle",
		}
		if err := WriteEnvFile(path, envMap); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d", len(lines))
		}
		if lines[0] != "AAA=first" || lines[1] != "MMM=middle" || lines[2] != "ZZZ=last" {
			t.Errorf("unexpected output order: %v", lines)
		}
	})

	t.Run("0600 permissions", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "output.env")
		if err := WriteEnvFile(path, map[string]string{"K": "v"}); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected 0600, got %04o", perm)
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
