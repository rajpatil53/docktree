package compose

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rajpatil53/docktree/internal/identity"
)

const (
	SharedNetworkKey = "docktree_shared"
	ProxyNetworkKey  = "docktree_proxy"
	ManualProfile    = "docktree-manual"

	DefaultProxyImage = "lucaslorentz/caddy-docker-proxy:2.11-alpine"
	ProxyDataVolume   = "docktree_caddy_data"
	ProxyConfigVolume = "docktree_caddy_config"

	DefaultUplinkImage  = "alpine/socat:1.8.0.3"
	UplinkServicePrefix = "dt-uplink"
	UpstreamAliasPrefix = "dt-upstream-"

	// ProxyNetworkLabel marks the docktree-managed per-project proxy networks so
	// the proxy convergence can discover every one Caddy must attach to.
	ProxyNetworkLabel = "com.docktree.proxy"

	LabelManaged = "com.docktree.managed"
	LabelApp     = "com.docktree.app"
	LabelSlug    = "com.docktree.slug"
	LabelProject = "com.docktree.project"
	LabelService = "com.docktree.service"
	LabelTier    = "com.docktree.tier"
	LabelFork    = "com.docktree.fork"
	LabelData    = "com.docktree.data"
)

type Options struct {
	App              string
	Slug             string
	MainSlug         string
	Project          string
	InfraProject     string
	SharedNetwork    string
	GeneratedEnvFile string
	InfraEnvFile     string
	UplinkImage      string
	Shared           []string
	Services         map[string]ServiceOptions
	Forked           map[string]ForkOptions
	StatefulModes    map[string]string
	Proxy            ProxyOptions
}

type ProxyOptions struct {
	Enabled   bool
	DNSSuffix string
	HTTPSPort int
}

type ServiceOptions struct {
	HostPortEnv       string
	Fixed             bool
	Autostart         bool
	AutostartExplicit bool
	Expose            string
	ProxyPort         int
}

type ForkOptions struct {
	VolumeName   string
	SourceVolume string
}

func RenderInfra(model *Model, opts Options) ([]byte, error) {
	return renderProjection(model, opts.withDefaults(), true)
}

func RenderWorktree(model *Model, opts Options) ([]byte, error) {
	return renderProjection(model, opts.withDefaults(), false)
}

func (o Options) withDefaults() Options {
	if o.GeneratedEnvFile == "" {
		o.GeneratedEnvFile = ".env.worktree"
	}
	if o.InfraEnvFile == "" {
		o.InfraEnvFile = ".env.infra"
	}
	if o.Project == "" && o.App != "" && o.Slug != "" {
		o.Project = o.App + "-" + o.Slug
	}
	if o.MainSlug == "" {
		o.MainSlug = "main"
	}
	if o.InfraProject == "" && o.App != "" {
		o.InfraProject = o.App + "-infra"
	}
	if o.SharedNetwork == "" && o.App != "" {
		o.SharedNetwork = o.App + "_shared"
	}
	if o.UplinkImage == "" {
		o.UplinkImage = DefaultUplinkImage
	}
	if o.Services == nil {
		o.Services = map[string]ServiceOptions{}
	}
	if o.Forked == nil {
		o.Forked = map[string]ForkOptions{}
	}
	if o.StatefulModes == nil {
		o.StatefulModes = map[string]string{}
	}
	return o
}

func renderProjection(model *Model, opts Options, infra bool) ([]byte, error) {
	if model == nil {
		return nil, fmt.Errorf("nil compose model")
	}
	for name := range model.Services {
		// Generated names are always dt-uplink-<slug>[.<svc>]; reserve exactly
		// that namespace so e.g. "dt-uplinker" stays usable.
		if name == UplinkServicePrefix || strings.HasPrefix(name, UplinkServicePrefix+"-") || strings.HasPrefix(name, UplinkServicePrefix+".") {
			return nil, fmt.Errorf("service name %q collides with docktree's reserved %q namespace; rename the service", name, UplinkServicePrefix)
		}
	}

	shared := sharedSet(opts.Shared)
	uplinked := subtractForked(shared, opts.Forked)
	services := map[string]any{}
	resourceNeeds := newResourceNeeds()

	var uplinkServices map[string]map[string]any
	uplinkFor := map[string]string{}
	if !infra {
		var err error
		uplinkServices, uplinkFor, err = renderUplinkServices(model, opts, uplinked)
		if err != nil {
			return nil, err
		}
	}

	for name, service := range model.Services {
		fork, forked := opts.Forked[name]
		includeWorktree := !shared[name] || forked
		includeInfra := shared[name]
		if (infra && !includeInfra) || (!infra && !includeWorktree) {
			continue
		}
		rawService := cloneMap(service.Raw)
		delete(rawService, "container_name")

		if infra {
			ensureInfraNetworks(rawService, name)
			// Infra services consume the worktree-INVARIANT artifact: feeding
			// them per-worktree env would change their compose config hash on
			// every cross-worktree up and bounce the shared databases.
			appendEnvFile(rawService, opts.InfraEnvFile)
			applyServiceLabels(rawService, opts, name, "infra", opts.InfraProject)
			if opts.Proxy.Enabled && ServiceProxied(opts, name, service) {
				applyProxyLabels(rawService, opts, name, service, ProxyNetworkName(opts.InfraProject))
			}
		} else {
			rewriteSharedDependsOn(rawService, uplinkFor)
			appendEnvFile(rawService, opts.GeneratedEnvFile)
			ensureDefaultNetwork(rawService)
			if forked {
				delete(rawService, "ports")
				if err := replaceNamedVolume(rawService, fork.SourceVolume, fork.VolumeName); err != nil {
					return nil, fmt.Errorf("fork %s: %w", name, err)
				}
			}
			if serviceOption, ok := opts.Services[name]; ok && serviceOption.AutostartExplicit && !serviceOption.Autostart {
				addProfile(rawService, ManualProfile)
			}
			applyServiceLabels(rawService, opts, name, "worktree", opts.Project)
			if dataMode := aggregateDataMode(opts.StatefulModes); dataMode != "" {
				setLabels(rawService, map[string]string{LabelData: dataMode})
			}
			if forked {
				setLabels(rawService, map[string]string{LabelFork: name})
			}
			if opts.Proxy.Enabled && ServiceProxied(opts, name, service) {
				applyProxyLabels(rawService, opts, name, service, ProxyNetworkName(opts.Project))
			}
		}

		services[name] = rawService
		resourceNeeds.addFromRawService(rawService)
	}

	for name, rawService := range uplinkServices {
		services[name] = rawService
		resourceNeeds.addFromRawService(rawService)
	}

	doc := map[string]any{
		"services": services,
	}
	if networks := renderNetworks(model, resourceNeeds.networks, opts); len(networks) > 0 {
		doc["networks"] = networks
	}
	if volumes := renderResources(model.Volumes, resourceNeeds.volumes, true); len(volumes) > 0 {
		doc["volumes"] = volumes
	}
	if configs := renderResources(model.Configs, resourceNeeds.configs, false); len(configs) > 0 {
		doc["configs"] = configs
	}
	if secrets := renderResources(model.Secrets, resourceNeeds.secrets, false); len(secrets) > 0 {
		doc["secrets"] = secrets
	}
	return yaml.Marshal(doc)
}

// UplinkServiceName returns the generated ambassador's compose service name.
// The slug suffix keeps its implicit alias unique on the shared network, so
// no generic name is ever ambiguous there.
func UplinkServiceName(slug string) string {
	return UplinkServicePrefix + "-" + slug
}

// UpstreamAlias returns the docktree-owned alias an infra shared service
// carries on the shared network. The uplink dials this name instead of the
// canonical one: resolving the canonical name from the dual-homed uplink
// would return the uplink's own default-network alias — a forwarding loop.
func UpstreamAlias(service string) string {
	return UpstreamAliasPrefix + service
}

// renderUplinkServices generates the per-worktree ambassador: a socat
// container dual-homed on the worktree default network (carrying the
// canonical aliases of every uplinked shared service) and the shared network
// (where it dials dt-upstream-* aliases). It is the only worktree container
// that joins the shared network.
func renderUplinkServices(model *Model, opts Options, uplinked map[string]bool) (map[string]map[string]any, map[string]string, error) {
	if len(uplinked) == 0 {
		return nil, nil, nil
	}
	names := make([]string, 0, len(uplinked))
	for name := range uplinked {
		names = append(names, name)
	}
	sort.Strings(names)
	ports := map[string][]int{}
	for _, name := range names {
		svc, ok := model.Services[name]
		if !ok {
			return nil, nil, fmt.Errorf("shared service %q is not defined in the compose config; remove it from docktree.toml shared or add the service", name)
		}
		servicePorts, err := uplinkPorts(svc)
		if err != nil {
			return nil, nil, err
		}
		ports[name] = servicePorts
	}

	// One listener cannot serve two names on one IP: a service whose port
	// collides with one already claimed in the primary ambassador gets a
	// dedicated ambassador of its own (separate container, separate IP).
	claimed := map[int]bool{}
	var primary, dedicated []string
	for _, name := range names {
		collides := false
		for _, port := range ports[name] {
			if claimed[port] {
				collides = true
				break
			}
		}
		if collides {
			dedicated = append(dedicated, name)
			continue
		}
		for _, port := range ports[name] {
			claimed[port] = true
		}
		primary = append(primary, name)
	}

	out := map[string]map[string]any{}
	uplinkFor := make(map[string]string, len(names))
	if len(primary) > 0 {
		uplinkName := UplinkServiceName(opts.Slug)
		out[uplinkName] = buildUplinkService(opts, uplinkName, primary, ports)
		for _, name := range primary {
			uplinkFor[name] = uplinkName
		}
	}
	for _, name := range dedicated {
		// Extends the slug-unique primary name with a dot segment (slugs are
		// dot-free), so a dedicated name can never equal another worktree's
		// primary or dedicated uplink name.
		uplinkName := UplinkServiceName(opts.Slug) + "." + name
		out[uplinkName] = buildUplinkService(opts, uplinkName, []string{name}, ports)
		uplinkFor[name] = uplinkName
	}
	return out, uplinkFor, nil
}

// uplinkPorts derives the container ports the ambassador forwards for one
// shared service: declared TCP port targets, deduplicated and sorted. A
// shared service with no derivable ports is an error — silently skipping it
// would leave its consumers resolving nothing at runtime.
func uplinkPorts(svc Service) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, port := range svc.Ports {
		if port.Target <= 0 || strings.EqualFold(port.Protocol, "udp") || seen[port.Target] {
			continue
		}
		seen[port.Target] = true
		out = append(out, port.Target)
	}
	for _, item := range listFromAny(svc.Raw["expose"]) {
		targets, err := parseExposeEntry(stringFromAny(item))
		if err != nil {
			return nil, fmt.Errorf("shared service %q: %w", svc.Name, err)
		}
		for _, target := range targets {
			if target <= 0 || seen[target] {
				continue
			}
			seen[target] = true
			out = append(out, target)
		}
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("shared service %q declares no TCP ports; the worktree uplink cannot forward it (declare its container port under ports or expose)", svc.Name)
	}
	return out, nil
}

// parseExposeEntry parses one compose expose entry — "5432", "4222/tcp",
// "8000-8002[/proto]" — into TCP port targets. UDP entries yield nothing
// (the uplink is TCP-only); oversized ranges are an error rather than a
// container with dozens of forwarders.
func parseExposeEntry(entry string) ([]int, error) {
	spec := strings.TrimSpace(entry)
	if spec == "" {
		return nil, nil
	}
	if before, proto, ok := strings.Cut(spec, "/"); ok {
		if strings.EqualFold(proto, "udp") {
			return nil, nil
		}
		spec = before
	}
	first, last, isRange := strings.Cut(spec, "-")
	start := intFromAny(first)
	if !isRange {
		if start <= 0 {
			return nil, fmt.Errorf("expose entry %q is not a valid port", entry)
		}
		return []int{start}, nil
	}
	end := intFromAny(last)
	if start <= 0 || end < start {
		return nil, fmt.Errorf("expose entry %q is not a valid port range", entry)
	}
	const maxExposeRange = 32
	if end-start+1 > maxExposeRange {
		return nil, fmt.Errorf("expose range %q spans more than %d ports; declare the uplinked ports individually", entry, maxExposeRange)
	}
	out := make([]int, 0, end-start+1)
	for port := start; port <= end; port++ {
		out = append(out, port)
	}
	return out, nil
}

func buildUplinkService(opts Options, uplinkName string, serviceNames []string, ports map[string][]int) map[string]any {
	aliases := make([]any, 0, len(serviceNames))
	var forwards, probes []string
	for _, svc := range serviceNames {
		aliases = append(aliases, svc)
		for _, port := range ports[svc] {
			forwards = append(forwards, fmt.Sprintf("socat TCP-LISTEN:%d,fork,reuseaddr TCP:%s:%d &", port, UpstreamAlias(svc), port))
			// Two probes per port: the upstream (infra reachable) and the local
			// listener (a crashed forwarder must turn the container unhealthy —
			// `wait` only returns once ALL backgrounded socats exit).
			probes = append(probes, fmt.Sprintf("socat -T 2 /dev/null TCP:%s:%d", UpstreamAlias(svc), port))
			probes = append(probes, fmt.Sprintf("socat -T 2 /dev/null TCP:127.0.0.1:%d", port))
		}
	}
	raw := map[string]any{
		"image":      opts.UplinkImage,
		"entrypoint": []any{"/bin/sh", "-ec", strings.Join(forwards, "\n") + "\nwait\n"},
		"restart":    "unless-stopped",
		"networks": map[string]any{
			"default":        map[string]any{"aliases": aliases},
			SharedNetworkKey: nil,
		},
		// The health probe dials the real upstreams: an L4 forwarder accepts
		// TCP even when infra is down, so container health is the honest
		// readiness signal for dependents.
		"healthcheck": map[string]any{
			"test":     []any{"CMD-SHELL", strings.Join(probes, " && ")},
			"interval": "3s",
			"timeout":  fmt.Sprintf("%ds", 2*len(probes)+2),
			"retries":  5,
		},
	}
	applyServiceLabels(raw, opts, uplinkName, "uplink", opts.Project)
	return raw
}

func subtractForked(shared map[string]bool, forked map[string]ForkOptions) map[string]bool {
	out := map[string]bool{}
	for name, isShared := range shared {
		if isShared {
			if _, ok := forked[name]; !ok {
				out[name] = true
			}
		}
	}
	return out
}

func sharedSet(shared []string) map[string]bool {
	out := make(map[string]bool, len(shared))
	for _, name := range shared {
		out[name] = true
	}
	return out
}

type resourceNeeds struct {
	networks map[string]bool
	volumes  map[string]bool
	configs  map[string]bool
	secrets  map[string]bool
}

func newResourceNeeds() resourceNeeds {
	return resourceNeeds{
		networks: map[string]bool{},
		volumes:  map[string]bool{},
		configs:  map[string]bool{},
		secrets:  map[string]bool{},
	}
}

func (n resourceNeeds) addFromRawService(raw map[string]any) {
	for _, name := range parseNetworkNames(raw["networks"]) {
		n.networks[name] = true
	}
	for _, volume := range parseVolumes(raw["volumes"]) {
		if volume.Type == "volume" && volume.Source != "" {
			n.volumes[volume.Source] = true
		}
	}
	for _, config := range parseResourceRefs(raw["configs"]) {
		if config.Source != "" {
			n.configs[config.Source] = true
		}
	}
	for _, secret := range parseResourceRefs(raw["secrets"]) {
		if secret.Source != "" {
			n.secrets[secret.Source] = true
		}
	}
	// Top-level secrets are also referenced from build-time secrets
	// (build.secrets), not just runtime mounts.
	for _, secret := range parseResourceRefs(mapFromAny(raw["build"])["secrets"]) {
		if secret.Source != "" {
			n.secrets[secret.Source] = true
		}
	}
}

func renderNetworks(model *Model, needed map[string]bool, opts Options) map[string]any {
	// Per-project proxy networks are docktree-managed and created out of band
	// (by the proxy convergence), so the stack references them as external.
	proxyNetworks := map[string]bool{}
	if opts.Project != "" {
		proxyNetworks[ProxyNetworkName(opts.Project)] = true
	}
	if opts.InfraProject != "" {
		proxyNetworks[ProxyNetworkName(opts.InfraProject)] = true
	}
	out := map[string]any{}
	for name := range needed {
		if name == SharedNetworkKey {
			out[name] = map[string]any{
				"name":     opts.SharedNetwork,
				"external": true,
			}
			continue
		}
		if proxyNetworks[name] {
			out[name] = map[string]any{
				"name":     name,
				"external": true,
			}
			continue
		}
		if resource, ok := model.Networks[name]; ok {
			out[name] = cloneMap(resource.Raw)
		} else {
			out[name] = map[string]any{}
		}
	}
	return out
}

func renderResources(resources map[string]Resource, needed map[string]bool, labelOwned bool) map[string]any {
	out := map[string]any{}
	for name := range needed {
		resource, ok := resources[name]
		if !ok {
			if labelOwned {
				out[name] = map[string]any{
					"name":     name,
					"external": true,
				}
			}
			continue
		}
		raw := cloneMap(resource.Raw)
		if labelOwned && !resource.External && resource.Name == "" {
			setLabels(raw, map[string]string{LabelManaged: "true"})
		}
		out[name] = raw
	}
	return out
}

// rewriteSharedDependsOn replaces depends_on edges onto uplinked shared
// services with a single edge onto the worktree's uplink ambassador,
// condition service_healthy: the uplink's health probes the real upstreams,
// so dependents wait for a live path to infra rather than losing the edge
// outright. Services with no shared edges keep their depends_on untouched
// (including list form).
func rewriteSharedDependsOn(raw map[string]any, uplinkFor map[string]string) {
	value, ok := raw["depends_on"]
	if !ok {
		return
	}
	next := map[string]any{}
	touched := false
	addUplinkEdge := func(name string, required, hasRequired bool) {
		edge := map[string]any{"condition": "service_healthy"}
		if hasRequired && !required {
			// An author-declared optional dependency stays optional.
			edge["required"] = false
		}
		if existing, ok := next[name].(map[string]any); ok {
			// Two shared edges collapsing into one uplink edge: the edge is
			// optional only if every collapsed dependency was optional.
			if existing["required"] != false {
				delete(edge, "required")
			}
		}
		next[name] = edge
		touched = true
	}
	switch deps := value.(type) {
	case map[string]any:
		for name, config := range deps {
			if uplink, ok := uplinkFor[name]; ok {
				required, hasRequired := true, false
				if configMap, ok := config.(map[string]any); ok {
					if value, ok := boolFromAny(configMap["required"]); ok {
						required, hasRequired = value, true
					}
				}
				addUplinkEdge(uplink, required, hasRequired)
				continue
			}
			next[name] = cloneAny(config)
		}
	case []any:
		for _, item := range deps {
			name := stringFromAny(item)
			if name == "" {
				continue
			}
			if uplink, ok := uplinkFor[name]; ok {
				addUplinkEdge(uplink, true, false)
				continue
			}
			next[name] = map[string]any{"condition": "service_started"}
		}
	default:
		return
	}
	if !touched {
		return
	}
	raw["depends_on"] = next
}

func appendEnvFile(raw map[string]any, path string) {
	var files []any
	for _, item := range listFromAny(raw["env_file"]) {
		switch v := item.(type) {
		case string:
			if v == path {
				continue
			}
			files = append(files, v)
		case map[string]any:
			if stringFromAny(v["path"]) == path {
				continue
			}
			files = append(files, cloneMap(v))
		default:
			files = append(files, cloneAny(v))
		}
	}
	files = append(files, path)
	raw["env_file"] = files
}

func ensureDefaultNetwork(raw map[string]any) {
	networks := serviceNetworkMap(raw["networks"])
	if _, ok := networks["default"]; !ok {
		networks["default"] = nil
	}
	raw["networks"] = networks
}

// ensureInfraNetworks attaches an infra shared service to the shared network
// with its docktree-owned upstream alias, which worktree uplinks dial instead
// of the canonical name (see UpstreamAlias).
func ensureInfraNetworks(raw map[string]any, serviceName string) {
	networks := serviceNetworkMap(raw["networks"])
	networks[SharedNetworkKey] = map[string]any{
		"aliases": []any{UpstreamAlias(serviceName)},
	}
	raw["networks"] = networks
}

func serviceNetworkMap(value any) map[string]any {
	out := map[string]any{}
	switch networks := value.(type) {
	case map[string]any:
		for name, config := range networks {
			out[name] = cloneAny(config)
		}
	case []any:
		for _, item := range networks {
			if name := stringFromAny(item); name != "" {
				out[name] = nil
			}
		}
	case string:
		out[networks] = nil
	}
	return out
}

func applyServiceLabels(raw map[string]any, opts Options, serviceName, tier, project string) {
	slug := opts.Slug
	if tier == "infra" {
		slug = opts.MainSlug
	}
	setLabels(raw, map[string]string{
		LabelManaged: "true",
		LabelApp:     opts.App,
		LabelSlug:    slug,
		LabelProject: project,
		LabelService: serviceName,
		LabelTier:    tier,
	})
}

// IsHTTPPort reports whether a container target port is one docktree treats as
// HTTP-exposable. Mirrors internal/dockerstate's conservative allowlist.
func IsHTTPPort(target int) bool {
	switch target {
	case 80, 443, 3000, 3001, 5000, 5173, 8000, 8080, 8081, 9000:
		return true
	default:
		return false
	}
}

// ServiceProxied reports whether a service should be routed through the reverse
// proxy (and therefore drop its host port). It is the single classifier shared by
// render (both projections) and the CLI URL surface so they always agree.
//
// Stateful services are excluded unconditionally — even with an explicit
// expose="http" — because a forked stateful service, though rendered into the
// worktree projection, must keep its published host port: clients dial it as a
// database, not over HTTP, and routing it through Caddy would yield a hostname
// with no usable backing.
//
// A shared service is a data tier by default and is excluded too, but it may opt
// into HTTP exposure with an explicit expose="http" (e.g. MinIO's S3 API, which
// serves browser-facing object URLs). Only then is it proxied; it is rendered in
// the infra projection, which now emits proxy labels for the opted-in case. The
// port heuristic never auto-proxies a shared service — opting in is explicit.
func ServiceProxied(opts Options, name string, svc Service) bool {
	if _, ok := opts.StatefulModes[name]; ok {
		return false
	}
	if sharedSet(opts.Shared)[name] {
		return opts.Services[name].Expose == "http"
	}
	switch opts.Services[name].Expose {
	case "http":
		return true
	case "none":
		return false
	}
	for _, p := range svc.Ports {
		if p.Published != "" && IsHTTPPort(p.Target) {
			return true
		}
	}
	return false
}

// ProxyHost returns the stable hostname a proxied service is reachable at,
// e.g. api-feature-x.shop.localhost (primary worktree: api.shop.localhost).
//
// The worktree slug rides as a hyphen suffix on the service label — the leftmost
// DNS label — rather than as its own dotted segment. This keeps the per-worktree
// variation inside a single leftmost label so one wildcard redirect URI
// (<service>-*.<app>.<suffix>, e.g. for WorkOS) covers every linked worktree.
//
// A shared service is excepted: it has a single instance per app (in the infra
// projection, not one per worktree), so it gets the slug-less <service>.<app>.<suffix>.
// Every worktree then renders the same stable host and the same caddy label for
// the one infra container, rather than each clobbering it with its own slug.
func ProxyHost(opts Options, service string) string {
	suffix := opts.Proxy.DNSSuffix
	if suffix == "" {
		suffix = "localhost"
	}
	app := identity.DNSLabel(opts.App)
	host := identity.DNSLabel(service)
	// In the primary worktree the slug is derived from the repo directory and so
	// equals the app; drop the redundant suffix (mirroring the project-name
	// collapse in resolve), leaving <service>.<app>.<suffix>.
	if slug := identity.DNSLabel(opts.Slug); slug != app && !sharedSet(opts.Shared)[service] {
		host += "-" + slug
	}
	return host + "." + app + "." + suffix
}

// ProxyURL returns the full https URL a proxied service is reachable at, e.g.
// https://minio.shop.localhost. The port is included only for a non-default
// HTTPS port (mirroring the CLI URL surface so render, CLI, and the injected
// public_url_env all agree on one string).
func ProxyURL(opts Options, service string) string {
	url := "https://" + ProxyHost(opts, service)
	if p := opts.Proxy.HTTPSPort; p != 0 && p != 443 {
		url += fmt.Sprintf(":%d", p)
	}
	return url
}

func proxyUpstreamPort(so ServiceOptions, svc Service) int {
	if so.ProxyPort > 0 {
		return so.ProxyPort
	}
	for _, p := range svc.Ports {
		if IsHTTPPort(p.Target) {
			return p.Target
		}
	}
	if len(svc.Ports) > 0 {
		return svc.Ports[0].Target
	}
	// A service force-exposed via expose="http" with no declared ports: assume
	// the conventional HTTP port rather than emit an unroutable {{upstreams 0}}.
	return 80
}

// ProxyNetworkName is the per-project proxy network a project's HTTP-exposed
// services join. Keying it to the compose project (not a single machine-global
// network) keeps each project's bare service-name DNS aliases (api, web, …)
// isolated, so identically-named services across apps/worktrees never resolve
// to one another. The single Caddy proxy attaches to every one of these.
func ProxyNetworkName(project string) string {
	return project + "_proxy"
}

func applyProxyLabels(raw map[string]any, opts Options, name string, svc Service, network string) {
	setLabels(raw, map[string]string{
		"caddy":               ProxyHost(opts, name),
		"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %d}}", proxyUpstreamPort(opts.Services[name], svc)),
	})
	networks := serviceNetworkMap(raw["networks"])
	networks[network] = nil
	raw["networks"] = networks
	delete(raw, "ports")
}

// ProxyRenderOptions parameterizes the machine-global proxy compose document.
type ProxyRenderOptions struct {
	Image string
	// Networks are every per-project proxy network the single Caddy proxy must
	// attach to (and treat as ingress) so backends on any of them stay routable.
	Networks  []string
	HTTPPort  int
	HTTPSPort int
}

// RenderProxy generates the machine-global docktree-proxy compose document. It
// is independent of any worktree: it owns the single Caddy reverse proxy that
// binds host 80/443 and routes every app/worktree HTTP service by hostname.
func RenderProxy(opts ProxyRenderOptions) ([]byte, error) {
	if opts.Image == "" {
		opts.Image = DefaultProxyImage
	}
	// Sort a copy so the rendered document (and therefore its compose config
	// hash) is independent of network discovery order — otherwise the proxy
	// container would churn on every up.
	networks := append([]string(nil), opts.Networks...)
	if len(networks) == 0 {
		networks = []string{ProxyNetworkKey}
	}
	sort.Strings(networks)
	if opts.HTTPPort == 0 {
		opts.HTTPPort = 80
	}
	if opts.HTTPSPort == 0 {
		opts.HTTPSPort = 443
	}
	networkRefs := make([]any, len(networks))
	networkDefs := map[string]any{}
	for i, n := range networks {
		networkRefs[i] = n
		networkDefs[n] = map[string]any{"name": n, "external": true}
	}
	doc := map[string]any{
		"services": map[string]any{
			"docktree-proxy": map[string]any{
				"image":       opts.Image,
				"restart":     "unless-stopped",
				"environment": map[string]any{"CADDY_INGRESS_NETWORKS": strings.Join(networks, ",")},
				"ports": []any{
					fmt.Sprintf("%d:80", opts.HTTPPort),
					fmt.Sprintf("%d:443", opts.HTTPSPort),
				},
				"volumes": []any{
					"/var/run/docker.sock:/var/run/docker.sock:ro",
					ProxyDataVolume + ":/data",
					ProxyConfigVolume + ":/config",
				},
				"networks": networkRefs,
				"labels": map[string]any{
					LabelManaged: "true",
					LabelService: "docktree-proxy",
					LabelTier:    "proxy",
				},
				"healthcheck": map[string]any{
					"test":     []any{"CMD", "wget", "-qO-", "http://localhost:2019/config/"},
					"interval": "5s",
					"timeout":  "3s",
					"retries":  12,
				},
			},
		},
		"networks": networkDefs,
		"volumes": map[string]any{
			ProxyDataVolume:   map[string]any{"name": ProxyDataVolume},
			ProxyConfigVolume: map[string]any{"name": ProxyConfigVolume},
		},
	}
	return yaml.Marshal(doc)
}

func aggregateDataMode(modes map[string]string) string {
	for _, mode := range modes {
		if mode == "isolated" {
			return "isolated"
		}
	}
	return ""
}

func setLabels(raw map[string]any, labels map[string]string) {
	out := parseLabels(raw["labels"])
	for key, value := range labels {
		if value != "" {
			out[key] = value
		}
	}
	raw["labels"] = out
}

func addProfile(raw map[string]any, profile string) {
	profiles := parseStringSlice(raw["profiles"])
	for _, existing := range profiles {
		if existing == profile {
			raw["profiles"] = profiles
			return
		}
	}
	raw["profiles"] = append(profiles, profile)
}

func replaceNamedVolume(raw map[string]any, sourceVolume, volumeName string) error {
	if volumeName == "" {
		return nil
	}
	items := listFromAny(raw["volumes"])
	if len(items) == 0 {
		if sourceVolume != "" {
			return fmt.Errorf("source volume %q not mounted", sourceVolume)
		}
		return nil
	}
	replaced := false
	next := make([]any, 0, len(items))
	for _, item := range items {
		if replaced {
			next = append(next, cloneAny(item))
			continue
		}
		switch v := item.(type) {
		case map[string]any:
			copy := cloneMap(v)
			source := stringFromAny(copy["source"])
			if stringFromAny(copy["type"]) == "volume" && source != "" && (sourceVolume == "" || source == sourceVolume) {
				copy["source"] = volumeName
				replaced = true
			}
			next = append(next, copy)
		case string:
			parts := splitVolumeSpec(v)
			if len(parts) >= 2 && !isBindSource(parts[0]) && (sourceVolume == "" || parts[0] == sourceVolume) {
				parts[0] = volumeName
				replaced = true
				next = append(next, joinVolumeSpec(parts))
			} else {
				next = append(next, v)
			}
		default:
			next = append(next, cloneAny(v))
		}
	}
	raw["volumes"] = next
	if sourceVolume != "" && !replaced {
		return fmt.Errorf("source volume %q not mounted", sourceVolume)
	}
	return nil
}

func splitVolumeSpec(value string) []string {
	return strings.Split(value, ":")
}

func joinVolumeSpec(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ":"
		}
		out += part
	}
	return out
}

func isBindSource(source string) bool {
	return source == "" || source == "." || source[0] == '.' || source[0] == '/'
}
