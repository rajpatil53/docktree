package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/dbmode"
	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/ports"
	"github.com/rajpatil53/docktree/internal/registry"
	"github.com/rajpatil53/docktree/internal/stateful"
)

// resolved is everything commands derive from config, identity, Docker labels,
// and the soft registry.
type resolved struct {
	wd                  string
	manifest            *config.Config
	slug                string
	mainSlug            string
	mainWorktree        bool
	mode                string
	names               identity.Names
	ownDevDB            string
	mainDevDB           string
	devDB               string
	testDB              string
	servicePorts        map[string]int
	sharedPorts         map[string]int
	primaryDefaultPorts map[string]bool
	statefulModes       map[string]string
	proxied             map[string]bool
}

func registryPath() string {
	home, _ := os.UserHomeDir()
	return paths.RegistryFile(home)
}

func dbName(m *config.Config, slug string) string {
	return dbNameWithMain(m, slug, "")
}

func dbNameWithMain(m *config.Config, slug, mainSlug string) string {
	if m.DB.NameTemplate != "" {
		name := strings.ReplaceAll(m.DB.NameTemplate, "{app}", m.App)
		name = strings.ReplaceAll(name, "{slug}", slug)
		name = strings.ReplaceAll(name, "{main_slug}", mainSlug)
		return name
	}
	return m.App + "_" + slug
}

func resolve(wd string) (*resolved, error) {
	return resolveWithFree(wd, ports.PortFree)
}

func resolveWithFree(wd string, free func(int) bool) (*resolved, error) {
	m, err := config.LoadFile(filepath.Join(wd, "docktree.toml"))
	if err != nil {
		return nil, err
	}
	slug := identity.ComputeSlug(filepath.Base(wd))
	if err := identity.ValidateSlug(slug); err != nil {
		return nil, err
	}
	n := identity.Derive(m.App, slug)

	mainSlug, mainWorktree := resolveMainSlugAndPrimaryInDir(wd, slug)
	if err := identity.ValidateSlug(mainSlug); err != nil {
		return nil, err
	}
	if mainWorktree {
		n.Project = m.App
	}

	ownDevDB := dbNameWithMain(m, slug, mainSlug)
	mainDevDB := dbNameWithMain(m, mainSlug, mainSlug)
	testDB := ownDevDB + m.DB.TestSuffix
	mode := "shared"
	devDB := dbmode.EffectiveDevDB(mode, ownDevDB, mainDevDB)
	n.DevDB, n.TestDB = devDB, testDB

	existingPorts, err := persistedPortEnv(wd, n.Project)
	if err != nil {
		return nil, err
	}
	servicePorts, err := resolveServicePortsWithExisting(m, slug, free, existingPorts)
	if err != nil {
		return nil, err
	}
	sharedPorts, err := resolveSharedPortsWithFree(m, free)
	if err != nil {
		return nil, err
	}

	return &resolved{
		wd:            wd,
		manifest:      m,
		slug:          slug,
		mainSlug:      mainSlug,
		mainWorktree:  mainWorktree,
		mode:          mode,
		names:         n,
		ownDevDB:      ownDevDB,
		mainDevDB:     mainDevDB,
		devDB:         devDB,
		testDB:        testDB,
		servicePorts:  servicePorts,
		sharedPorts:   sharedPorts,
		statefulModes: defaultStatefulModes(m),
	}, nil
}

func defaultStatefulModes(m *config.Config) map[string]string {
	modes := map[string]string{}
	for name := range m.Stateful {
		modes[name] = "shared"
	}
	return modes
}

func setStatefulMode(r *resolved, service, mode string) {
	if r.statefulModes == nil {
		r.statefulModes = map[string]string{}
	}
	r.statefulModes[service] = mode
}

func statefulMode(r *resolved, service string) string {
	if r.statefulModes != nil {
		if mode := r.statefulModes[service]; mode != "" {
			return mode
		}
	}
	return "shared"
}

func statefulDestroyRequest(r *resolved, service string, kind stateful.ResourceKind, resource string) stateful.DestroyRequest {
	st := r.manifest.Stateful[service]
	rendered := r.manifest.RenderTemplates(config.TemplateContext{App: r.manifest.App, Slug: r.slug, MainSlug: r.mainSlug})
	if renderedStateful, ok := rendered.Stateful[service]; ok {
		st.SnapshotSource = renderedStateful.SnapshotSource
		st.SourceDB = renderedStateful.SourceDB
	}
	return stateful.DestroyRequest{
		App:            r.manifest.App,
		Slug:           r.slug,
		MainSlug:       r.mainSlug,
		Service:        service,
		Strategy:       statefulMode(r, service),
		ResourceKind:   kind,
		ResourceName:   resource,
		SnapshotSource: st.SnapshotSource,
		SourceDB:       st.SourceDB,
	}
}

func resolveServicePorts(m *config.Config, slug string) (map[string]int, error) {
	return resolveServicePortsWithFree(m, slug, ports.PortFree)
}

func resolveServicePortsWithFree(m *config.Config, slug string, free func(int) bool) (map[string]int, error) {
	return resolveServicePortsWithExisting(m, slug, free, nil)
}

func resolveServicePortsWithExisting(m *config.Config, slug string, free func(int) bool, existing map[string]int) (map[string]int, error) {
	app, err := registry.EnsureApp(registryPath(), m.App, defaultWorktreeServiceBands(m))
	if err != nil {
		return nil, err
	}

	resolved := map[string]int{}
	for _, name := range sortedServiceNames(m.Services) {
		if isSharedService(m, name) {
			continue
		}
		svc := m.Services[name]
		if svc.HostPortEnv == "" {
			continue
		}
		if port := existing[svc.HostPortEnv]; validPort(port) {
			resolved[name] = port
			continue
		}
		base, ok := app.Bands[name]
		if !ok {
			continue
		}
		port, err := ports.Resolve(base, slug, registry.BandWidth, svc.Fixed, free)
		if err != nil {
			return nil, err
		}
		resolved[name] = port
	}
	return resolved, nil
}

func resolveSharedPorts(m *config.Config) (map[string]int, error) {
	return resolveSharedPortsWithFree(m, ports.PortFree)
}

func resolveSharedPortsWithFree(m *config.Config, free func(int) bool) (map[string]int, error) {
	canonical := map[string]int{}
	for _, name := range m.Shared {
		svc, ok := m.Services[name]
		if !ok || svc.HostPortEnv == "" {
			continue
		}
		if port := canonicalSharedPort(name); port > 0 {
			canonical[name] = port
		}
	}
	if len(canonical) == 0 {
		return map[string]int{}, nil
	}
	return registry.EnsureSharedPorts(registryPath(), m.App, canonical, free)
}

func canonicalSharedPort(service string) int {
	switch service {
	case "postgres", "postgresql":
		return 5432
	case "redis":
		return 6379
	case "opensearch", "elasticsearch":
		return 9200
	default:
		return 0
	}
}

type portInput struct {
	Service string
	Env     string
	Target  int
	Default int
	Shared  bool
	Fixed   bool
}

func resolvePortInputs(registryFile, app, slug, mainSlug string, inputs []portInput, free func(int) bool, existing map[string]int) (map[string]int, map[string]int, map[string]bool, error) {
	seed := map[string]int{}
	base := registry.BandStart
	for _, input := range inputs {
		if input.Shared || input.Env == "" {
			continue
		}
		if _, ok := seed[input.Service]; !ok {
			seed[input.Service] = base
			base += registry.BandWidth
		}
	}

	appRegistry, err := registry.EnsureApp(registryFile, app, seed)
	if err != nil {
		return nil, nil, nil, err
	}
	worktree := map[string]int{}
	sharedCanonical := map[string]int{}
	primaryDefaults := map[string]bool{}
	for _, input := range inputs {
		if input.Env == "" {
			continue
		}
		if input.Shared {
			if input.Target > 0 {
				sharedCanonical[input.Service] = input.Target
			}
			continue
		}
		if slug == mainSlug && validPort(input.Default) {
			if existing[input.Env] == input.Default {
				worktree[input.Service] = input.Default
				primaryDefaults[input.Service] = true
				continue
			}
			if !portFreeOrDefault(free, input.Default) {
				return nil, nil, nil, fmt.Errorf("port %d for main worktree service %q is taken; free it or change its Compose port default", input.Default, input.Service)
			}
			worktree[input.Service] = input.Default
			primaryDefaults[input.Service] = true
			continue
		}
		if port := existing[input.Env]; validPort(port) {
			worktree[input.Service] = port
			continue
		}
		base, ok := appRegistry.Bands[input.Service]
		if !ok {
			continue
		}
		port, err := ports.Resolve(base, slug, registry.BandWidth, input.Fixed, free)
		if err != nil {
			return nil, nil, nil, err
		}
		worktree[input.Service] = port
	}

	shared := map[string]int{}
	if len(sharedCanonical) > 0 {
		shared, err = registry.EnsureSharedPorts(registryFile, app, sharedCanonical, free)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return worktree, shared, primaryDefaults, nil
}

func portFreeOrDefault(free func(int) bool, port int) bool {
	if free == nil {
		return ports.PortFree(port)
	}
	return free(port)
}

func persistedPortEnv(wd, project string) (map[string]int, error) {
	values, err := envfile.Read(paths.EnvFile(wd))
	if err != nil {
		return nil, err
	}
	if values["COMPOSE_PROJECT_NAME"] != project {
		return map[string]int{}, nil
	}
	portsByEnv := map[string]int{}
	for key, value := range values {
		port, err := strconv.Atoi(value)
		if err == nil && validPort(port) {
			portsByEnv[key] = port
		}
	}
	return portsByEnv, nil
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func discoveredPortInputs(m *config.Config, model *compose.Model) []portInput {
	if model == nil {
		return nil
	}
	shared := sharedServiceSet(m)
	var inputs []portInput
	for _, serviceName := range sortedComposeServiceNames(model.Services) {
		service := model.Services[serviceName]
		cfg := m.Services[serviceName]
		for _, port := range service.Ports {
			token, ok := ports.ParseHostPortToken(port.Published)
			if !ok {
				continue
			}
			inputs = append(inputs, portInput{
				Service: serviceName,
				Env:     token.Env,
				Target:  port.Target,
				Default: token.Default,
				Shared:  shared[serviceName],
				Fixed:   cfg.Fixed,
			})
		}
	}
	return inputs
}

func defaultWorktreeServiceBands(m *config.Config) map[string]int {
	seed := map[string]int{}
	base := registry.BandStart
	for _, name := range sortedServiceNames(m.Services) {
		if isSharedService(m, name) {
			continue
		}
		if m.Services[name].HostPortEnv == "" {
			continue
		}
		seed[name] = base
		base += registry.BandWidth
	}
	return seed
}

func sortedServiceNames(services map[string]config.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isSharedService(m *config.Config, name string) bool {
	return sharedServiceSet(m)[name]
}

func sharedServiceSet(m *config.Config) map[string]bool {
	shared := map[string]bool{}
	for _, name := range m.Shared {
		shared[name] = true
	}
	return shared
}

func sortedComposeServiceNames(services map[string]compose.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
