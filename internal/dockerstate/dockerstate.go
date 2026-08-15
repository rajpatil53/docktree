// Package dockerstate normalizes Docker and Docker Compose runtime output into
// Docktree stack records. Docker labels are the runtime source of truth.
package dockerstate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
)

const (
	LabelManaged = "com.docktree.managed"
	LabelApp     = "com.docktree.app"
	LabelSlug    = "com.docktree.slug"
	LabelProject = "com.docktree.project"
	LabelService = "com.docktree.service"
	LabelTier    = "com.docktree.tier"
	LabelFork    = "com.docktree.fork"
	LabelData    = "com.docktree.data"
)

type Identity struct {
	App     string
	Slug    string
	Project string
}

type Stack struct {
	App      string    `json:"app"`
	Slug     string    `json:"slug"`
	Branch   string    `json:"branch,omitempty"`
	Project  string    `json:"project"`
	Status   string    `json:"status"`
	Services []Service `json:"services,omitempty"`
	URLs     []URL     `json:"urls,omitempty"`
	Managed  bool      `json:"-"`
}

type Service struct {
	App      string `json:"app,omitempty"`
	Slug     string `json:"slug,omitempty"`
	Project  string `json:"project,omitempty"`
	Name     string `json:"service"`
	State    string `json:"state,omitempty"`
	Status   string `json:"status,omitempty"`
	Health   string `json:"health,omitempty"`
	Image    string `json:"image,omitempty"`
	Tier     string `json:"tier,omitempty"`
	ForkMode string `json:"fork_mode,omitempty"`
	Ports    []Port `json:"ports,omitempty"`
	Managed  bool   `json:"-"`
}

type Port struct {
	Service   string `json:"service,omitempty"`
	Target    int    `json:"target"`
	Published int    `json:"published"`
	HostIP    string `json:"host_ip,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	URL       string `json:"url,omitempty"`
}

type URL struct {
	Service string `json:"service"`
	URL     string `json:"url"`
}

type composeLSRecord struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

type dockerPSRecord struct {
	ID     string `json:"ID"`
	Name   string `json:"Name"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Labels any    `json:"Labels"`
	Ports  any    `json:"Ports"`
}

type composePSRecord struct {
	Name       string          `json:"Name"`
	Service    string          `json:"Service"`
	Image      string          `json:"Image"`
	State      string          `json:"State"`
	Status     string          `json:"Status"`
	Health     string          `json:"Health"`
	Publishers []publisherJSON `json:"Publishers"`
}

type publisherJSON struct {
	URL           any `json:"URL"`
	HostIP        any `json:"HostIP"`
	TargetPort    any `json:"TargetPort"`
	PublishedPort any `json:"PublishedPort"`
	Protocol      any `json:"Protocol"`
}

func ParseComposeLSJSON(data []byte) ([]Stack, error) {
	records, err := parseJSONRecords[composeLSRecord](data)
	if err != nil {
		return nil, err
	}
	stacks := make([]Stack, 0, len(records))
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		stacks = append(stacks, Stack{Project: record.Name, Status: record.Status})
	}
	sortStacks(stacks)
	return stacks, nil
}

func ParseDockerPSJSON(data []byte) ([]Service, error) {
	records, err := parseJSONRecords[dockerPSRecord](data)
	if err != nil {
		return nil, err
	}
	services := make([]Service, 0, len(records))
	for _, record := range records {
		labels := parseLabels(record.Labels)
		project := firstLabel(labels, LabelProject, "docktree.project", "com.docker.compose.project")
		serviceName := firstLabel(labels, LabelService, "docktree.service", "com.docker.compose.service")
		if serviceName == "" {
			serviceName = record.Name
			if serviceName == "" {
				serviceName = record.Names
			}
		}
		service := Service{
			App:      firstLabel(labels, LabelApp, "docktree.app"),
			Slug:     firstLabel(labels, LabelSlug, "docktree.slug"),
			Project:  project,
			Name:     serviceName,
			State:    record.State,
			Status:   record.Status,
			Image:    record.Image,
			Tier:     firstLabel(labels, LabelTier, "docktree.tier"),
			ForkMode: forkMode(labels, serviceName),
			Ports:    parsePorts(record.Ports),
			Managed:  isManaged(labels),
		}
		for i := range service.Ports {
			service.Ports[i].Service = service.Name
		}
		services = append(services, service)
	}
	sortServices(services)
	return services, nil
}

func ParseComposePSJSON(data []byte, identity Identity) ([]Service, error) {
	records, err := parseJSONRecords[composePSRecord](data)
	if err != nil {
		return nil, err
	}
	services := make([]Service, 0, len(records))
	for _, record := range records {
		name := record.Service
		if name == "" {
			name = record.Name
		}
		service := Service{
			App:     identity.App,
			Slug:    identity.Slug,
			Project: identity.Project,
			Name:    name,
			State:   record.State,
			Status:  record.Status,
			Health:  record.Health,
			Image:   record.Image,
			Ports:   portsFromPublishers(record.Publishers),
		}
		for i := range service.Ports {
			service.Ports[i].Service = service.Name
		}
		services = append(services, service)
	}
	sortServices(services)
	return services, nil
}

func Merge(stacks []Stack, services []Service) []Stack {
	byProject := map[string]*Stack{}
	for i := range stacks {
		stack := stacks[i]
		if stack.Project == "" {
			continue
		}
		copy := stack
		byProject[stack.Project] = &copy
	}
	for _, service := range services {
		project := service.Project
		if project == "" {
			continue
		}
		stack, ok := byProject[project]
		if !ok {
			stack = &Stack{Project: project}
			byProject[project] = stack
		}
		if stack.App == "" {
			stack.App = service.App
		}
		if stack.Slug == "" {
			stack.Slug = service.Slug
		}
		if service.Managed {
			stack.Managed = true
		}
		stack.Services = append(stack.Services, service)
		for _, port := range service.Ports {
			if port.URL != "" {
				stack.URLs = append(stack.URLs, URL{Service: service.Name, URL: port.URL})
			}
		}
	}
	out := make([]Stack, 0, len(byProject))
	for _, stack := range byProject {
		sortServices(stack.Services)
		sortURLs(stack.URLs)
		out = append(out, *stack)
	}
	sortStacks(out)
	return out
}

func FindService(stacks []Stack, slug, service string) (Service, bool) {
	for _, stack := range stacks {
		if slug != "" && stack.Slug != slug {
			continue
		}
		for _, candidate := range stack.Services {
			if service == "" || candidate.Name == service {
				return candidate, true
			}
		}
	}
	return Service{}, false
}

func FindURL(stacks []Stack, slug, service string) (string, bool) {
	if svc, ok := FindService(stacks, slug, service); ok {
		for _, port := range svc.Ports {
			if port.URL != "" {
				return port.URL, true
			}
		}
	}
	return "", false
}

func parseJSONRecords[T any](data []byte) ([]T, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var records []T
		if err := json.Unmarshal([]byte(trimmed), &records); err != nil {
			return nil, err
		}
		return records, nil
	}
	var records []T
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func parseLabels(value any) map[string]string {
	out := map[string]string{}
	switch labels := value.(type) {
	case map[string]any:
		for key, raw := range labels {
			out[key] = stringAny(raw)
		}
	case string:
		for _, item := range strings.Split(labels, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
			if ok && key != "" {
				out[key] = value
			}
		}
	}
	return out
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
}

func isManaged(labels map[string]string) bool {
	return strings.EqualFold(firstLabel(labels, LabelManaged, "docktree.managed"), "true")
}

func forkMode(labels map[string]string, service string) string {
	if mode := firstLabel(labels, LabelData, "docktree.data", "com.docktree.data.mode", "docktree.data.mode"); mode != "" {
		return mode
	}
	if fork := firstLabel(labels, LabelFork, "docktree.fork"); fork != "" {
		if fork == service || service == "" {
			return "isolated"
		}
		return "isolated:" + fork
	}
	return "shared"
}

var dockerPortRe = regexp.MustCompile(`(?:(?:0\.0\.0\.0|127\.0\.0\.1|\[::\]|\*):)?([0-9]+)->([0-9]+)/(tcp|udp)`)

func parsePorts(value any) []Port {
	switch ports := value.(type) {
	case []any:
		return portsFromAnyList(ports)
	case string:
		return portsFromString(ports)
	default:
		if ports == nil {
			return nil
		}
		return portsFromString(stringAny(ports))
	}
}

func portsFromAnyList(items []any) []Port {
	var out []Port
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			target := intAny(firstAny(m, "TargetPort", "target", "Target"))
			published := intAny(firstAny(m, "PublishedPort", "published", "Published"))
			if target == 0 && published == 0 {
				continue
			}
			host := stringAny(firstAny(m, "URL", "HostIP", "host_ip"))
			protocol := stringAny(firstAny(m, "Protocol", "protocol"))
			out = append(out, newPort(target, published, host, protocol))
		}
	}
	return out
}

func portsFromPublishers(items []publisherJSON) []Port {
	var out []Port
	for _, item := range items {
		target := intAny(item.TargetPort)
		published := intAny(item.PublishedPort)
		if target == 0 && published == 0 {
			continue
		}
		host := stringAny(item.URL)
		if host == "" {
			host = stringAny(item.HostIP)
		}
		out = append(out, newPort(target, published, host, stringAny(item.Protocol)))
	}
	return out
}

func portsFromString(value string) []Port {
	var out []Port
	for _, match := range dockerPortRe.FindAllStringSubmatch(value, -1) {
		published, _ := strconv.Atoi(match[1])
		target, _ := strconv.Atoi(match[2])
		out = append(out, newPort(target, published, "", match[3]))
	}
	return out
}

func newPort(target, published int, host, protocol string) Port {
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" || host == "*" {
		host = "localhost"
	}
	port := Port{Target: target, Published: published, HostIP: host, Protocol: protocol}
	if published > 0 && isHTTPPort(protocol, target) {
		port.URL = fmt.Sprintf("http://%s:%d", host, published)
	}
	return port
}

func isHTTPPort(protocol string, target int) bool {
	if protocol != "" && !strings.EqualFold(protocol, "tcp") {
		return false
	}
	// Single source of truth for the HTTP-port allowlist, shared with the
	// compose proxy classifier so the two never drift.
	return compose.IsHTTPPort(target)
}

func firstAny(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func stringAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func intAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func sortStacks(stacks []Stack) {
	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Project < stacks[j].Project
	})
}

func sortServices(services []Service) {
	sort.Slice(services, func(i, j int) bool {
		if services[i].Project != services[j].Project {
			return services[i].Project < services[j].Project
		}
		return services[i].Name < services[j].Name
	})
}

func sortURLs(urls []URL) {
	sort.Slice(urls, func(i, j int) bool {
		if urls[i].Service != urls[j].Service {
			return urls[i].Service < urls[j].Service
		}
		return urls[i].URL < urls[j].URL
	})
}
