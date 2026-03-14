package config

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server Server            `yaml:"server"          json:"server"`
	Apps   map[string]App    `yaml:"apps,omitempty"  json:"apps,omitempty"`
	Stacks map[string]Stack  `yaml:"stacks,omitempty" json:"stacks,omitempty"`
}

type Server struct {
	Name         string `yaml:"name"          json:"name"`
	DeployDomain string `yaml:"deploy_domain" json:"deploy_domain"`
	StacksDir    string `yaml:"stacks_dir"    json:"stacks_dir"`
	GithubToken  string `yaml:"github_token"  json:"github_token,omitempty"`
}

type App struct {
	Repo     string         `yaml:"repo"              json:"repo"`
	Branch   string         `yaml:"branch"            json:"branch"`
	Domain   string         `yaml:"domain"            json:"domain"`
	EnvFile  string         `yaml:"env_file,omitempty"  json:"env_file,omitzero"`
	Compose  string         `yaml:"compose,omitempty"   json:"compose,omitzero"`
	Override string         `yaml:"override,omitempty"  json:"override,omitzero"`
	Secrets  []SecretRef    `yaml:"secrets,omitempty"   json:"secrets,omitzero"`
	Preview  *PreviewConfig `yaml:"preview,omitempty"   json:"preview,omitzero"`
}

type Stack struct {
	Path         string `yaml:"path"                    json:"path"`
	Domain       string `yaml:"domain"                  json:"domain"`
	AutoDeploy   bool   `yaml:"auto_deploy"             json:"auto_deploy,omitzero"`
	UpdateScript string `yaml:"update_script,omitempty" json:"update_script,omitzero"`
}

type SecretRef struct {
	Key    string `yaml:"key"    json:"key"`
	Type   string `yaml:"type"   json:"type"`
	Target string `yaml:"target" json:"target"`
}

type PreviewConfig struct {
	Enabled bool   `yaml:"enabled"           json:"enabled,omitzero"`
	Domain  string `yaml:"domain,omitempty"  json:"domain,omitzero"`
}

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// Load reads the YAML config file at path, applies defaults, expands env vars,
// and validates the result. Returns a descriptive error on any failure.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("config %s: %w", path, typeErr)
		}
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	// Expand ${VAR} in github_token only.
	original := cfg.Server.GithubToken
	cfg.Server.GithubToken = envVarRe.ReplaceAllStringFunc(cfg.Server.GithubToken, func(match string) string {
		varName := match[2 : len(match)-1]
		return os.Getenv(varName)
	})
	if cfg.Server.GithubToken == "" && strings.Contains(original, "${") {
		slog.Warn("github_token expanded to empty string", "original", original)
	}

	// Apply defaults for app fields.
	for name, app := range cfg.Apps {
		if app.Branch == "" {
			app.Branch = "main"
		}
		if app.Compose == "" {
			app.Compose = "compose.yml"
		}
		cfg.Apps[name] = app
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Server.Name == "" {
		return errors.New("server.name is required")
	}
	if cfg.Server.DeployDomain == "" {
		return errors.New("server.deploy_domain is required")
	}
	if cfg.Server.StacksDir == "" {
		return errors.New("server.stacks_dir is required")
	}

	// Collect all domains to detect duplicates across apps and stacks.
	domains := make(map[string]string) // domain -> "app:name" or "stack:name"

	for _, name := range slices.Sorted(maps.Keys(cfg.Apps)) {
		app := cfg.Apps[name]
		if app.Repo == "" {
			return fmt.Errorf("app %q: repo is required", name)
		}
		if app.Domain == "" {
			return fmt.Errorf("app %q: domain is required", name)
		}
		if prev, exists := domains[app.Domain]; exists {
			return fmt.Errorf("app %q: domain %q already used by %s", name, app.Domain, prev)
		}
		domains[app.Domain] = "app:" + name

		for i, sec := range app.Secrets {
			if sec.Type != "env" && sec.Type != "docker-secret" {
				return fmt.Errorf("app %q: secrets[%d]: type must be \"env\" or \"docker-secret\", got %q", name, i, sec.Type)
			}
		}

		if app.Preview != nil && app.Preview.Enabled {
			if app.Preview.Domain == "" {
				return fmt.Errorf("app %q: preview.domain is required when preview.enabled is true", name)
			}
			if !strings.Contains(app.Preview.Domain, "*") {
				return fmt.Errorf("app %q: preview.domain must contain a wildcard (*)", name)
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(cfg.Stacks)) {
		stack := cfg.Stacks[name]
		if stack.Path == "" {
			return fmt.Errorf("stack %q: path is required", name)
		}
		if stack.Domain == "" {
			return fmt.Errorf("stack %q: domain is required", name)
		}
		if prev, exists := domains[stack.Domain]; exists {
			return fmt.Errorf("stack %q: domain %q already used by %s", name, stack.Domain, prev)
		}
		domains[stack.Domain] = "stack:" + name
	}

	return nil
}
