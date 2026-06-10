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
	Server Server           `yaml:"server"           json:"server"`
	Stacks map[string]Stack `yaml:"stacks,omitempty" json:"stacks,omitempty"`
}

type Server struct {
	Name         string `yaml:"name"           json:"name"`
	DeployDomain string `yaml:"deploy_domain"  json:"deploy_domain"`
	ServicesDir  string `yaml:"services_dir"   json:"services_dir"`
	GithubToken  string `yaml:"github_token"   json:"github_token,omitempty"`
	AcmeEmail    string `yaml:"acme_email"     json:"acme_email,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitzero"`
	// Bind is the listen address. Empty means all interfaces (":port"). Set to
	// "127.0.0.1" to bind loopback-only when Caddy runs on the same host.
	Bind string `yaml:"bind,omitempty" json:"bind,omitzero"`
}

type Stack struct {
	// Source (mutually exclusive)
	Repo string `yaml:"repo,omitempty" json:"repo,omitzero"`
	Path string `yaml:"path,omitempty" json:"path,omitzero"`

	// Git tracking (repo: stacks only)
	Branch     string `yaml:"branch,omitempty"      json:"branch,omitzero"`
	Tag        string `yaml:"tag,omitempty"          json:"tag,omitzero"`
	TagPattern string `yaml:"tag_pattern,omitempty"  json:"tag_pattern,omitzero"`

	// Routing
	Domain string `yaml:"domain" json:"domain"`

	// Compose
	Compose  string `yaml:"compose,omitempty"  json:"compose,omitzero"`
	Override string `yaml:"override,omitempty" json:"override,omitzero"`

	// Environment
	EnvFile    string `yaml:"env_file,omitempty" json:"env_file,omitzero"`
	ConfigFile string `yaml:"config,omitempty"   json:"config,omitzero"`

	// Secrets
	Secrets []SecretRef `yaml:"secrets,omitempty" json:"secrets,omitzero"`

	// Preview (repo: stacks only)
	Preview *PreviewConfig `yaml:"preview,omitempty" json:"preview,omitzero"`

	// Path-stack specific
	AutoDeploy   bool   `yaml:"auto_deploy,omitempty" json:"auto_deploy,omitzero"`
	UpdateScript string `yaml:"update,omitempty"      json:"update,omitzero"`

	// Public status page opt-in. A stack appears on the public availability page
	// by name only when Availability.Public is true; everything else stays private.
	Availability *AvailabilityConfig `yaml:"availability,omitempty" json:"availability,omitzero"`
}

// AvailabilityConfig controls a stack's presence on the public status page.
type AvailabilityConfig struct {
	Public bool `yaml:"public" json:"public"`
}

type SecretRef struct {
	Key      string `yaml:"key"                json:"key"`
	Type     string `yaml:"type"               json:"type"`
	Target   string `yaml:"target"             json:"target"`
	Generate string `yaml:"generate,omitempty" json:"generate,omitzero"`
	Length   int    `yaml:"length,omitempty"   json:"length,omitzero"`
}

type PreviewConfig struct {
	Enabled bool   `yaml:"enabled"          json:"enabled,omitzero"`
	Domain  string `yaml:"domain,omitempty" json:"domain,omitzero"`
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

	// Apply defaults for repo stacks.
	for name, stack := range cfg.Stacks {
		if stack.Repo != "" {
			if stack.Branch == "" && stack.Tag == "" {
				stack.Branch = "main"
			}
			if stack.Compose == "" {
				stack.Compose = "compose.yml"
			}
			cfg.Stacks[name] = stack
		}
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

var stackNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validateRelativePath rejects absolute paths and any ".." traversal segment,
// confining a path to the IaC repo. Used for stack config and path sources.
func validateRelativePath(stackName, field, p string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("stack %q: %s must be a relative path within the IaC repo, got %q", stackName, field, p)
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return fmt.Errorf("stack %q: %s must be a relative path within the IaC repo, got %q", stackName, field, p)
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

	// Collect all domains to detect duplicates across stacks.
	domains := make(map[string]string) // domain -> "stack:name"

	repoRe := regexp.MustCompile(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`)

	for _, name := range slices.Sorted(maps.Keys(cfg.Stacks)) {
		stack := cfg.Stacks[name]

		// Stack names become filesystem directories and docker compose project
		// names, so restrict them to a safe lowercase set.
		if !stackNameRe.MatchString(name) {
			return fmt.Errorf("stack %q: name must match %s (lowercase alphanumeric, '-', '_')", name, stackNameRe.String())
		}

		// Exactly one source must be set.
		if stack.Repo == "" && stack.Path == "" {
			return fmt.Errorf("stack %q: one of repo or path is required", name)
		}
		if stack.Repo != "" && stack.Path != "" {
			return fmt.Errorf("stack %q: repo and path are mutually exclusive", name)
		}

		// Domain required and unique.
		if stack.Domain == "" {
			return fmt.Errorf("stack %q: domain is required", name)
		}
		if prev, exists := domains[stack.Domain]; exists {
			return fmt.Errorf("stack %q: domain %q already used by %s", name, stack.Domain, prev)
		}
		domains[stack.Domain] = "stack:" + name

		// Secret validation.
		for i, sec := range stack.Secrets {
			if sec.Type != "env" && sec.Type != "docker-secret" {
				return fmt.Errorf("stack %q: secrets[%d]: type must be \"env\" or \"docker-secret\", got %q", name, i, sec.Type)
			}
			if err := validateSecretRefGenerate("stack", name, i, sec); err != nil {
				return err
			}
		}

		if stack.Repo != "" {
			if !repoRe.MatchString(stack.Repo) {
				return fmt.Errorf("stack %q: repo %q must be in owner/name format with alphanumeric characters", name, stack.Repo)
			}
			if stack.Tag != "" && stack.Branch != "" {
				return fmt.Errorf("stack %q: tag and branch are mutually exclusive", name)
			}
			if stack.Tag == "" && stack.Branch == "" {
				return fmt.Errorf("stack %q: one of branch or tag is required", name)
			}
			if stack.TagPattern != "" && stack.Tag != "" {
				return fmt.Errorf("stack %q: tag_pattern requires branch (not compatible with tag)", name)
			}
			if stack.TagPattern != "" {
				if _, err := path.Match(stack.TagPattern, ""); err != nil {
					return fmt.Errorf("stack %q: tag_pattern %q is not a valid glob: %w", name, stack.TagPattern, err)
				}
			}
			if stack.ConfigFile != "" {
				if err := validateRelativePath(name, "config", stack.ConfigFile); err != nil {
					return err
				}
			}
			if stack.Preview != nil && stack.Preview.Enabled {
				if stack.Preview.Domain == "" {
					return fmt.Errorf("stack %q: preview.domain is required when preview.enabled is true", name)
				}
				if !strings.Contains(stack.Preview.Domain, "*") {
					return fmt.Errorf("stack %q: preview.domain must contain a wildcard (*)", name)
				}
			}
		}

		if stack.Path != "" {
			if err := validateRelativePath(name, "path", stack.Path); err != nil {
				return err
			}
			if stack.Branch != "" || stack.Tag != "" || stack.TagPattern != "" {
				return fmt.Errorf("stack %q: branch, tag, and tag_pattern are not valid for path stacks", name)
			}
			if stack.Preview != nil {
				return fmt.Errorf("stack %q: preview is not valid for path stacks", name)
			}
			if stack.Compose != "" {
				return fmt.Errorf("stack %q: compose is not valid for path stacks", name)
			}
		}
	}

	return nil
}
