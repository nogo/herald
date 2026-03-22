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
	EnvFile  OverrideList      `yaml:"env_file,omitempty"`
	Secrets  []string          `yaml:"secrets,omitempty"`
	Networks []string          `yaml:"networks,omitempty"`
}

// OverrideList is a string slice that marshals with the !override YAML tag.
// Docker Compose uses !override to replace (not merge) a list from the base file.
type OverrideList []string

// MarshalYAML emits the list with the !override tag so Docker Compose replaces
// rather than appends to the base compose file's value.
func (o OverrideList) MarshalYAML() (any, error) {
	node := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!override",
	}
	for _, s := range o {
		node.Content = append(node.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: s,
		})
	}
	return node, nil
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

// DeepMergeYAML merges overlay YAML bytes into base YAML bytes, preserving
// YAML tags (e.g. !override). Returns the merged YAML bytes.
// Only mapping nodes are merged recursively; all other node types from the
// overlay replace the base value entirely (including their tags).
func DeepMergeYAML(base, overlay []byte) ([]byte, error) {
	var baseDoc, overlayDoc yaml.Node
	if err := yaml.Unmarshal(base, &baseDoc); err != nil {
		return nil, fmt.Errorf("parsing base YAML: %w", err)
	}
	if err := yaml.Unmarshal(overlay, &overlayDoc); err != nil {
		return nil, fmt.Errorf("parsing overlay YAML: %w", err)
	}
	// Unmarshal wraps content in a document node.
	if baseDoc.Kind == yaml.DocumentNode && len(baseDoc.Content) > 0 {
		mergeNodes(baseDoc.Content[0], overlayDoc.Content[0])
	}
	return yaml.Marshal(&baseDoc)
}

// mergeNodes recursively merges src into dst. Both must be mapping nodes
// for recursive merge; otherwise src replaces dst.
func mergeNodes(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		*dst = *src
		return
	}
	// Build index of dst keys → value node index.
	dstIdx := make(map[string]int, len(dst.Content)/2)
	for i := 0; i < len(dst.Content)-1; i += 2 {
		dstIdx[dst.Content[i].Value] = i + 1
	}
	for i := 0; i < len(src.Content)-1; i += 2 {
		key := src.Content[i]
		val := src.Content[i+1]
		if vi, ok := dstIdx[key.Value]; ok {
			// Key exists in dst — recurse if both are mappings, else replace.
			mergeNodes(dst.Content[vi], val)
		} else {
			// New key — append.
			dst.Content = append(dst.Content, key, val)
		}
	}
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
