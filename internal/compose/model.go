// Package compose parses Docker Compose's resolved JSON model and renders the
// Docktree-owned compose projections.
package compose

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type Model struct {
	Services map[string]Service
	Networks map[string]Resource
	Volumes  map[string]Resource
	Configs  map[string]Resource
	Secrets  map[string]Resource
	raw      map[string]any
}

type Service struct {
	Name          string
	Raw           map[string]any
	DependsOn     map[string]Dependency
	EnvFile       []string
	Networks      []string
	Ports         []Port
	Volumes       []VolumeMount
	Configs       []ResourceRef
	Secrets       []ResourceRef
	ContainerName string
	Labels        map[string]string
	Profiles      []string
}

type Dependency struct {
	Condition string
	Required  bool
}

type Port struct {
	Target    int
	Published string
	Protocol  string
}

type VolumeMount struct {
	Type   string
	Source string
	Target string
}

type ResourceRef struct {
	Source string
	Target string
}

type Resource struct {
	Raw      map[string]any
	Name     string
	External bool
}

func ParseConfigJSON(data []byte) (*Model, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	model := &Model{
		Services: map[string]Service{},
		Networks: map[string]Resource{},
		Volumes:  map[string]Resource{},
		Configs:  map[string]Resource{},
		Secrets:  map[string]Resource{},
		raw:      cloneMap(raw),
	}

	for name, rawService := range mapFromAny(raw["services"]) {
		serviceMap, ok := rawService.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compose service %q is %T, want object", name, rawService)
		}
		service := Service{
			Name:          name,
			Raw:           cloneMap(serviceMap),
			DependsOn:     parseDependsOn(serviceMap["depends_on"]),
			EnvFile:       parseEnvFile(serviceMap["env_file"]),
			Networks:      parseNetworkNames(serviceMap["networks"]),
			Ports:         parsePorts(serviceMap["ports"]),
			Volumes:       parseVolumes(serviceMap["volumes"]),
			Configs:       parseResourceRefs(serviceMap["configs"]),
			Secrets:       parseResourceRefs(serviceMap["secrets"]),
			ContainerName: stringFromAny(serviceMap["container_name"]),
			Labels:        parseLabels(serviceMap["labels"]),
			Profiles:      parseStringSlice(serviceMap["profiles"]),
		}
		model.Services[name] = service
	}

	model.Networks = parseResources(raw["networks"])
	model.Volumes = parseResources(raw["volumes"])
	model.Configs = parseResources(raw["configs"])
	model.Secrets = parseResources(raw["secrets"])
	return model, nil
}

func parseDependsOn(value any) map[string]Dependency {
	out := map[string]Dependency{}
	switch v := value.(type) {
	case map[string]any:
		for name, depValue := range v {
			dep := Dependency{Required: true}
			if depMap, ok := depValue.(map[string]any); ok {
				dep.Condition = stringFromAny(depMap["condition"])
				if required, ok := boolFromAny(depMap["required"]); ok {
					dep.Required = required
				}
			}
			out[name] = dep
		}
	case []any:
		for _, item := range v {
			if name := stringFromAny(item); name != "" {
				out[name] = Dependency{Required: true}
			}
		}
	}
	return out
}

func parseEnvFile(value any) []string {
	var out []string
	for _, item := range listFromAny(value) {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if path := stringFromAny(v["path"]); path != "" {
				out = append(out, path)
			}
		}
	}
	return out
}

func parseNetworkNames(value any) []string {
	var out []string
	switch v := value.(type) {
	case map[string]any:
		for name := range v {
			out = append(out, name)
		}
	case []any:
		for _, item := range v {
			if name := stringFromAny(item); name != "" {
				out = append(out, name)
			}
		}
	case string:
		out = append(out, v)
	}
	return out
}

func parsePorts(value any) []Port {
	var out []Port
	for _, item := range listFromAny(value) {
		switch v := item.(type) {
		case map[string]any:
			out = append(out, Port{
				Target:    intFromAny(v["target"]),
				Published: stringFromAny(v["published"]),
				Protocol:  stringFromAny(v["protocol"]),
			})
		case string:
			out = append(out, parsePortString(v))
		}
	}
	return out
}

func parsePortString(value string) Port {
	spec := strings.TrimSpace(value)
	protocol := ""
	if before, after, ok := strings.Cut(spec, "/"); ok {
		spec = before
		protocol = after
	}
	parts := splitPortSpec(spec)
	if len(parts) == 1 {
		return Port{Target: intFromAny(parts[0]), Protocol: protocol}
	}
	return Port{
		Published: parts[len(parts)-2],
		Target:    intFromAny(parts[len(parts)-1]),
		Protocol:  protocol,
	}
}

func splitPortSpec(value string) []string {
	var parts []string
	start := 0
	braceDepth := 0
	bracketDepth := 0
	for i, r := range value {
		switch r {
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ':':
			if braceDepth == 0 && bracketDepth == 0 {
				parts = append(parts, value[start:i])
				start = i + len(string(r))
			}
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func parseVolumes(value any) []VolumeMount {
	var out []VolumeMount
	for _, item := range listFromAny(value) {
		switch v := item.(type) {
		case map[string]any:
			out = append(out, VolumeMount{
				Type:   stringFromAny(v["type"]),
				Source: stringFromAny(v["source"]),
				Target: stringFromAny(v["target"]),
			})
		case string:
			parts := strings.Split(v, ":")
			if len(parts) >= 2 && !strings.HasPrefix(parts[0], ".") && !strings.HasPrefix(parts[0], "/") {
				out = append(out, VolumeMount{Type: "volume", Source: parts[0], Target: parts[1]})
			}
		}
	}
	return out
}

func parseResourceRefs(value any) []ResourceRef {
	var out []ResourceRef
	for _, item := range listFromAny(value) {
		switch v := item.(type) {
		case string:
			out = append(out, ResourceRef{Source: v})
		case map[string]any:
			source := stringFromAny(v["source"])
			if source == "" {
				source = stringFromAny(v["target"])
			}
			out = append(out, ResourceRef{Source: source, Target: stringFromAny(v["target"])})
		}
	}
	return out
}

func parseResources(value any) map[string]Resource {
	out := map[string]Resource{}
	for name, rawResource := range mapFromAny(value) {
		resourceMap, _ := rawResource.(map[string]any)
		resource := Resource{Raw: cloneMap(resourceMap)}
		resource.Name = stringFromAny(resourceMap["name"])
		if external, ok := boolFromAny(resourceMap["external"]); ok {
			resource.External = external
		}
		out[name] = resource
	}
	return out
}

func parseLabels(value any) map[string]string {
	out := map[string]string{}
	switch v := value.(type) {
	case map[string]any:
		for key, rawValue := range v {
			out[key] = stringFromAny(rawValue)
		}
	case map[string]string:
		for key, rawValue := range v {
			out[key] = rawValue
		}
	case []any:
		for _, item := range v {
			label := stringFromAny(item)
			key, value, ok := strings.Cut(label, "=")
			if ok {
				out[key] = value
			} else if key != "" {
				out[key] = ""
			}
		}
	}
	return out
}

func parseStringSlice(value any) []string {
	var out []string
	for _, item := range listFromAny(value) {
		if s := stringFromAny(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func listFromAny(value any) []any {
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return []any{v}
	}
}

func mapFromAny(value any) map[string]any {
	if value == nil {
		return nil
	}
	out, _ := value.(map[string]any)
	return out
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func boolFromAny(value any) (bool, bool) {
	v, ok := value.(bool)
	return v, ok
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, cloneAny(item))
		}
		return out
	default:
		return v
	}
}
