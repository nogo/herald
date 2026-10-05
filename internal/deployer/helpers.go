package deployer

import (
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nogo/herald/internal/compose"
)

// LoadConfigFile parses a KEY=VALUE env file, skipping comments and blank lines.
func LoadConfigFile(path string, logger *slog.Logger) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("config file %q not found in IaC repo", path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			logger.Debug("config file: ignoring line without '='", "line", trimmed)
			continue
		}
		result[strings.TrimSpace(trimmed[:idx])] = strings.TrimSpace(trimmed[idx+1:])
	}
	return result, nil
}

// BuildEnvMap returns the merged env map: config file base overlaid with resolved secret env vars.
// configFile is a relative path within iacRepoDir. If configFile is empty, envVars is returned as-is.
func BuildEnvMap(configFile, iacRepoDir string, envVars map[string]string, logger *slog.Logger) (map[string]string, error) {
	if configFile == "" {
		return envVars, nil
	}
	base, err := LoadConfigFile(filepath.Join(iacRepoDir, configFile), logger)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(base)+len(envVars))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range envVars {
		if _, exists := result[k]; exists {
			logger.Debug("config key overridden by secret", "key", k)
		}
		result[k] = v
	}
	return result, nil
}

// OverrideParams contains all the varying inputs for GenerateOverride.
type OverrideParams struct {
	DeployDir      string
	StackName      string
	Domain         string
	ComposeFile    string
	EnvFilePaths   []string // absolute paths to env files for the override
	DockerSecrets  map[string]string
	Service        string // explicit routing service from the stack; empty to detect
	Port           string // explicit container port from the stack; empty to detect
	InternalNet    string // e.g. "herald-myapp-internal"
	Upstream       string // fixed host:port or ip:port instead of the caddy network; empty to route to the container
	GatewayIP      string // Docker host address that "host" in Upstream resolves to
	InlineOverride string // raw YAML to deep-merge (from stack.Override)
}

// GenerateOverride creates a compose.override.yml for a stack.
// Returns marshaled YAML bytes; the caller writes them to disk.
func GenerateOverride(params OverrideParams) ([]byte, error) {
	route, upstreamHost, err := selectStackRoute(params.ComposeFile, params.Service, params.Port, params.Upstream)
	if err != nil {
		return nil, fmt.Errorf("stack %q: %w", params.StackName, err)
	}
	mainName, allNames := route.Service, route.Services

	svc := compose.ServiceOverride{
		Labels: map[string]string{
			"caddy":               params.Domain,
			"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %s}}", route.Port),
		},
		Networks: compose.OverrideList{"caddy", params.InternalNet},
	}
	networks := map[string]compose.NetworkDef{
		"caddy":            {External: true},
		params.InternalNet: {},
	}
	if params.Upstream != "" {
		if upstreamHost == compose.HostUpstream {
			upstreamHost = params.GatewayIP
		}
		svc.Labels["caddy.reverse_proxy"] = net.JoinHostPort(upstreamHost, route.Port)
		svc.Networks = nil
		delete(networks, "caddy")
	}

	if len(params.EnvFilePaths) > 0 {
		svc.EnvFile = compose.OverrideList(params.EnvFilePaths)
	}

	secretNames := slices.Sorted(maps.Keys(params.DockerSecrets))
	if len(secretNames) > 0 {
		svc.Secrets = secretNames
	}

	services := map[string]compose.ServiceOverride{mainName: svc}
	for _, name := range allNames {
		if name == mainName {
			continue
		}
		services[name] = compose.ServiceOverride{
			Networks: compose.OverrideList{params.InternalNet},
		}
	}

	override := compose.Override{
		Services: services,
		Networks: networks,
	}

	if len(secretNames) > 0 {
		override.Secrets = make(map[string]compose.SecretFileDef, len(secretNames))
		for _, name := range secretNames {
			override.Secrets[name] = compose.SecretFileDef{
				File: filepath.Join(params.DeployDir, "secrets", name),
			}
		}
	}

	data, err := yaml.Marshal(override)
	if err != nil {
		return nil, fmt.Errorf("marshaling override: %w", err)
	}

	if params.InlineOverride != "" {
		data, err = compose.DeepMergeYAML(data, []byte(params.InlineOverride))
		if err != nil {
			return nil, fmt.Errorf("merging inline override: %w", err)
		}
	}

	return data, nil
}

// WriteDockerSecrets writes each secret value to a file under secretsDir with 0600 permissions.
func WriteDockerSecrets(secretsDir string, dockerSecrets map[string]string) error {
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		return fmt.Errorf("creating secrets dir: %w", err)
	}
	secretsRoot, err := os.OpenRoot(secretsDir)
	if err != nil {
		return fmt.Errorf("opening secrets root: %w", err)
	}
	defer secretsRoot.Close()
	for name, val := range dockerSecrets {
		if err := compose.WriteSecret(secretsRoot, name, val); err != nil {
			return fmt.Errorf("writing docker secret %q: %w", name, err)
		}
	}
	return nil
}
