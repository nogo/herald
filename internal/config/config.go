package config

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   Server              `yaml:"server"             json:"server"`
	Apps     map[string]App      `yaml:"apps,omitempty"     json:"apps,omitempty"`
	Services map[string]Service  `yaml:"services,omitempty" json:"services,omitempty"`
}

type Server struct {
	Name         string `yaml:"name"          json:"name"`
	DeployDomain string `yaml:"deploy_domain" json:"deploy_domain"`
	ServicesDir  string `yaml:"services_dir"  json:"services_dir"`
	GithubToken  string `yaml:"github_token"  json:"github_token,omitempty"`
	AcmeEmail    string `yaml:"acme_email"    json:"acme_email,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitzero"`
}

type App struct {
	Repo       string         `yaml:"repo"                  json:"repo"`
	Branch     string         `yaml:"branch,omitempty"      json:"branch,omitzero"`
	Tag        string         `yaml:"tag,omitempty"         json:"tag,omitzero"`
	TagPattern string         `yaml:"tag_pattern,omitempty" json:"tag_pattern,omitzero"`
	Domain     string         `yaml:"domain"                json:"domain"`
	EnvFile    string         `yaml:"env_file,omitempty"    json:"env_file,omitzero"`
	ConfigFile string         `yaml:"config,omitempty"      json:"config,omitzero"`
	Compose    string         `yaml:"compose,omitempty"     json:"compose,omitzero"`
	Override   string         `yaml:"override,omitempty"    json:"override,omitzero"`
	Secrets    []SecretRef    `yaml:"secrets,omitempty"     json:"secrets,omitzero"`
	Preview    *PreviewConfig `yaml:"preview,omitempty"     json:"preview,omitzero"`
}

type Service struct {
	Path         string      `yaml:"path"                    json:"path"`
	Domain       string      `yaml:"domain"                  json:"domain"`
	AutoDeploy   bool        `yaml:"auto_deploy"             json:"auto_deploy,omitzero"`
	UpdateScript string      `yaml:"update_script,omitempty" json:"update_script,omitzero"`
	EnvFile      string      `yaml:"env_file,omitempty"      json:"env_file,omitzero"`
	ConfigFile   string      `yaml:"config,omitempty"        json:"config,omitzero"`
	Secrets      []SecretRef `yaml:"secrets,omitempty"       json:"secrets,omitzero"`
}

type SecretRef struct {
	Key      string `yaml:"key"               json:"key"`
	Type     string `yaml:"type"              json:"type"`
	Target   string `yaml:"target"            json:"target"`
	Generate string `yaml:"generate,omitempty" json:"generate,omitzero"`
	Length   int    `yaml:"length,omitempty"   json:"length,omitzero"`
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
		slog.Warn("github_token contains env var reference but expanded to empty string")
	}

	// Default acme_email to webmaster@<deploy_domain>.
	if cfg.Server.AcmeEmail == "" && cfg.Server.DeployDomain != "" {
		cfg.Server.AcmeEmail = "webmaster@" + cfg.Server.DeployDomain
	}

	// Default port.
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 9483
	}

	// Apply defaults for app fields.
	for name, app := range cfg.Apps {
		if app.Branch == "" && app.Tag == "" {
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

func validateSecretRefGenerate(kind, name string, i int, sec SecretRef) error {
	if sec.Generate != "" {
		switch sec.Generate {
		case "base64", "alphanumeric", "hex":
		default:
			return fmt.Errorf("%s %q: secrets[%d]: generate must be \"base64\", \"alphanumeric\", or \"hex\", got %q", kind, name, i, sec.Generate)
		}
	}
	if sec.Length != 0 {
		if sec.Generate == "" {
			return fmt.Errorf("%s %q: secrets[%d]: length requires generate to be set", kind, name, i)
		}
		if sec.Length < 16 || sec.Length > 512 {
			return fmt.Errorf("%s %q: secrets[%d]: length must be between 16 and 512, got %d", kind, name, i, sec.Length)
		}
	}
	return nil
}

func validate(cfg *Config) error {
	if cfg.Server.Name == "" {
		return errors.New("server.name is required")
	}
	if cfg.Server.DeployDomain == "" {
		return errors.New("server.deploy_domain is required")
	}
	if cfg.Server.ServicesDir == "" {
		return errors.New("server.services_dir is required")
	}

	// Collect all domains to detect duplicates across apps and services.
	domains := make(map[string]string) // domain -> "app:name" or "service:name"

	repoRe := regexp.MustCompile(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`)

	for _, name := range slices.Sorted(maps.Keys(cfg.Apps)) {
		app := cfg.Apps[name]
		if app.Repo == "" {
			return fmt.Errorf("app %q: repo is required", name)
		}
		if !repoRe.MatchString(app.Repo) {
			return fmt.Errorf("app %q: repo %q must be in owner/name format with alphanumeric characters", name, app.Repo)
		}
		if app.Domain == "" {
			return fmt.Errorf("app %q: domain is required", name)
		}
		if prev, exists := domains[app.Domain]; exists {
			return fmt.Errorf("app %q: domain %q already used by %s", name, app.Domain, prev)
		}
		domains[app.Domain] = "app:" + name

		// tag and branch are mutually exclusive
		if app.Tag != "" && app.Branch != "" {
			return fmt.Errorf("app %q: tag and branch are mutually exclusive", name)
		}
		// one of tag or branch is required
		if app.Tag == "" && app.Branch == "" {
			return fmt.Errorf("app %q: one of branch or tag is required", name)
		}
		// tag_pattern only valid alongside branch
		if app.TagPattern != "" && app.Tag != "" {
			return fmt.Errorf("app %q: tag_pattern requires branch (not compatible with tag)", name)
		}
		// validate tag_pattern is a legal glob
		if app.TagPattern != "" {
			if _, err := path.Match(app.TagPattern, ""); err != nil {
				return fmt.Errorf("app %q: tag_pattern %q is not a valid glob: %w", name, app.TagPattern, err)
			}
		}

		if app.ConfigFile != "" {
			if filepath.IsAbs(app.ConfigFile) {
				return fmt.Errorf("app %q: config must be a relative path within the IaC repo, got %q", name, app.ConfigFile)
			}
			for _, part := range strings.Split(filepath.ToSlash(app.ConfigFile), "/") {
				if part == ".." {
					return fmt.Errorf("app %q: config must be a relative path within the IaC repo, got %q", name, app.ConfigFile)
				}
			}
		}

		for i, sec := range app.Secrets {
			if sec.Type != "env" && sec.Type != "docker-secret" {
				return fmt.Errorf("app %q: secrets[%d]: type must be \"env\" or \"docker-secret\", got %q", name, i, sec.Type)
			}
			if err := validateSecretRefGenerate("app", name, i, sec); err != nil {
				return err
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

	for _, name := range slices.Sorted(maps.Keys(cfg.Services)) {
		svc := cfg.Services[name]
		if svc.Path == "" {
			return fmt.Errorf("service %q: path is required", name)
		}
		if svc.Domain == "" {
			return fmt.Errorf("service %q: domain is required", name)
		}
		if prev, exists := domains[svc.Domain]; exists {
			return fmt.Errorf("service %q: domain %q already used by %s", name, svc.Domain, prev)
		}
		domains[svc.Domain] = "service:" + name

		for i, sec := range svc.Secrets {
			if sec.Type != "env" && sec.Type != "docker-secret" {
				return fmt.Errorf("service %q: secrets[%d]: type must be \"env\" or \"docker-secret\", got %q", name, i, sec.Type)
			}
			if err := validateSecretRefGenerate("service", name, i, sec); err != nil {
				return err
			}
		}
	}

	return nil
}
