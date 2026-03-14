// Package compose provides shared types and helpers for Docker Compose override generation.
package compose

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Override is the top-level compose override structure.
type Override struct {
	Services map[string]ServiceOverride `yaml:"services"`
	Networks map[string]NetworkDef      `yaml:"networks,omitempty"`
	Secrets  map[string]SecretFileDef   `yaml:"secrets,omitempty"`
}

// ServiceOverride holds per-service overrides.
type ServiceOverride struct {
	Labels   map[string]string `yaml:"labels,omitempty"`
	EnvFile  []string          `yaml:"env_file,omitempty"`
	Secrets  []string          `yaml:"secrets,omitempty"`
	Networks []string          `yaml:"networks,omitempty"`
}

// NetworkDef declares a Docker network.
type NetworkDef struct {
	External bool `yaml:"external"`
}

// SecretFileDef points to a secret file on disk.
type SecretFileDef struct {
	File string `yaml:"file"`
}

// minimalService holds only the fields needed for port detection.
type minimalService struct {
	Expose []any `yaml:"expose"`
	Ports  []any `yaml:"ports"`
}

// minimalCompose holds only the fields needed for service detection.
type minimalCompose struct {
	Services map[string]minimalService `yaml:"services"`
}

// DetectServiceInfo parses a compose file to find the main service name and port.
// Prefers a service named "app", then appName, then the first alphabetically.
// defaultPort is returned when no port is found (e.g. "3000" for apps, "80" for stacks).
func DetectServiceInfo(filePath, appName, defaultPort string) (serviceName, port string, err error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}

	var mc minimalCompose
	if err := yaml.Unmarshal(data, &mc); err != nil {
		return "", "", err
	}

	if len(mc.Services) == 0 {
		return "app", defaultPort, nil
	}

	names := slices.Sorted(maps.Keys(mc.Services))
	serviceName = names[0]
	for _, n := range names {
		if n == "app" || n == appName {
			serviceName = n
			break
		}
	}

	port = extractFirstPort(mc.Services[serviceName])
	if port == "" {
		port = defaultPort
	}
	return serviceName, port, nil
}

func extractFirstPort(svc minimalService) string {
	for _, v := range svc.Expose {
		if p := portFromAny(v); p != "" {
			return p
		}
	}
	for _, v := range svc.Ports {
		if p := portFromAny(v); p != "" {
			return p
		}
	}
	return ""
}

func portFromAny(v any) string {
	switch val := v.(type) {
	case string:
		// "3000", "3000:3000", "0.0.0.0:80:3000"
		parts := strings.Split(val, ":")
		p := strings.TrimSpace(parts[len(parts)-1])
		return p
	case int:
		return fmt.Sprintf("%d", val)
	case map[string]any:
		// long form: {target: 3000, published: 3000}
		if t, ok := val["target"]; ok {
			return portFromAny(t)
		}
	}
	return ""
}

// DeepMerge recursively merges overlay into base. Overlay wins on conflict
// unless both values are maps, in which case they are merged recursively.
func DeepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if bv, ok := result[k]; ok {
			if bMap, ok := bv.(map[string]any); ok {
				if oMap, ok := v.(map[string]any); ok {
					result[k] = DeepMerge(bMap, oMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}

// WriteEnvFile writes KEY=value pairs to .env in the given root, sorted by key.
func WriteEnvFile(root *os.Root, envVars map[string]string) error {
	f, err := root.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, key := range slices.Sorted(maps.Keys(envVars)) {
		if _, err := fmt.Fprintf(f, "%s=%s\n", key, envVars[key]); err != nil {
			return err
		}
	}
	return nil
}

// WriteSecret writes a docker secret value to a named file in the given root.
func WriteSecret(root *os.Root, name, value string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(value)
	return err
}
