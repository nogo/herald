// Package compose provides shared types and helpers for Docker Compose override generation.
package compose

import (
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
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
	Networks OverrideList      `yaml:"networks,omitempty"`
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
	External bool `yaml:"external,omitempty"`
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

// Route is the service Caddy sends a stack's domain to, and the container port
// it listens on. Services lists every service in the compose file.
type Route struct {
	Service  string
	Port     string
	Services []string
}

// SelectRoute is the one place that decides where a stack's domain is routed.
// service and port are the stack's explicit choices and may be empty.
//
// Without service, it picks the sole service and otherwise fails asking for
// one: no service name is special, so a stack with several services always
// says which one serves its domain. Without port, it uses the service's single
// expose/ports target, defaultPort when it declares none, and fails when it
// declares several distinct targets. Errors name filePath.
func SelectRoute(filePath, service, port, defaultPort string) (Route, error) {
	if port != "" {
		if err := ValidatePort(port); err != nil {
			return Route{}, err
		}
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return Route{}, fmt.Errorf("reading compose file %s: %w", filePath, err)
	}
	var mc minimalCompose
	if err := yaml.Unmarshal(data, &mc); err != nil {
		return Route{}, fmt.Errorf("parsing compose file %s: %w", filePath, err)
	}
	names := slices.Sorted(maps.Keys(mc.Services))
	if len(names) == 0 {
		return Route{}, fmt.Errorf("compose file %s defines no services", filePath)
	}

	switch {
	case service != "":
		if _, ok := mc.Services[service]; !ok {
			return Route{}, fmt.Errorf("compose file %s: service %q not found (services: %s)", filePath, service, strings.Join(names, ", "))
		}
	case len(names) == 1:
		service = names[0]
	default:
		return Route{}, fmt.Errorf("compose file %s has several services (%s): set `service:` on the stack to the one to route the domain to", filePath, strings.Join(names, ", "))
	}

	if port == "" {
		targets := declaredPorts(mc.Services[service])
		switch len(targets) {
		case 0:
			port = defaultPort
		case 1:
			port = targets[0]
		default:
			return Route{}, fmt.Errorf("compose file %s: service %q declares several ports (%s): set `port:` on the stack to the one to route to", filePath, service, strings.Join(targets, ", "))
		}
	}
	return Route{Service: service, Port: port, Services: names}, nil
}

// HostUpstream is the upstream host that means "the Docker host", as in `host:8123`.
const HostUpstream = "host"

// ParseUpstream splits a stack's `upstream:` into its host and port. The host is
// HostUpstream or an IP address; the port is numeric, 1-65535.
func ParseUpstream(upstream string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(upstream)
	if err != nil {
		return "", "", fmt.Errorf("upstream %q must be host:<port> or <ip>:<port>", upstream)
	}
	if host != HostUpstream && net.ParseIP(host) == nil {
		return "", "", fmt.Errorf("upstream %q: host must be %q or an IP address", upstream, HostUpstream)
	}
	if err := ValidatePort(port); err != nil {
		return "", "", fmt.Errorf("upstream %q: %w", upstream, err)
	}
	return host, port, nil
}

// ValidatePort reports whether p is a numeric container port, 1-65535.
func ValidatePort(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q is not a container port between 1 and 65535", p)
	}
	return nil
}

// declaredPorts returns the distinct numeric container ports from a service's
// expose and ports entries, sorted. Entries it cannot read as a single port
// (ranges, variable interpolation) are ignored.
func declaredPorts(svc minimalService) []string {
	seen := map[string]bool{}
	for _, v := range slices.Concat(svc.Expose, svc.Ports) {
		p := portFromAny(v)
		if ValidatePort(p) == nil {
			seen[p] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func portFromAny(v any) string {
	switch val := v.(type) {
	case string:
		// "3000", "3000:3000", "0.0.0.0:80:3000", "3000/tcp"
		parts := strings.Split(val, ":")
		p, _, _ := strings.Cut(parts[len(parts)-1], "/")
		return strings.TrimSpace(p)
	case int:
		return strconv.Itoa(val)
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
		if strings.ContainsAny(envVars[key], "\r\n") {
			return fmt.Errorf("env value for %q contains a newline, which cannot be represented in a .env file", key)
		}
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
