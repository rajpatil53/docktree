package compose

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderInfraProjectionIncludesOnlySharedServicesAndResources(t *testing.T) {
	model := loadFixture(t, "config-basic.json")
	opts := Options{
		App:              "shop",
		Slug:             "feature",
		InfraProject:     "shop-infra",
		SharedNetwork:    "shop_shared",
		Shared:           []string{"postgres"},
		GeneratedEnvFile: ".env.worktree",
	}
	setMainSlug(t, &opts, "shop_main")

	out, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)

	services := asMap(t, doc["services"])
	if len(services) != 1 {
		t.Fatalf("services = %#v", services)
	}
	postgres := asMap(t, services["postgres"])
	if _, ok := services["api"]; ok {
		t.Fatalf("infra projection included non-shared api service")
	}
	if _, ok := postgres["container_name"]; ok {
		t.Fatalf("container_name should be stripped from infra service: %#v", postgres)
	}
	networks := keys(asMap(t, postgres["networks"]))
	if !contains(networks, SharedNetworkKey) || !contains(networks, "backplane") {
		t.Fatalf("postgres networks = %#v", networks)
	}
	upstreamAliases := asSlice(t, asMap(t, asMap(t, postgres["networks"])[SharedNetworkKey])["aliases"])
	if len(upstreamAliases) != 1 || upstreamAliases[0] != "dt-upstream-postgres" {
		t.Fatalf("postgres shared-net aliases = %#v, want [dt-upstream-postgres]", upstreamAliases)
	}

	topVolumes := asMap(t, doc["volumes"])
	if _, ok := topVolumes["pgdata"]; !ok || len(topVolumes) != 1 {
		t.Fatalf("infra volumes = %#v, want only pgdata", topVolumes)
	}
	topNetworks := asMap(t, doc["networks"])
	if _, ok := topNetworks["backplane"]; !ok {
		t.Fatalf("infra networks missing backplane: %#v", topNetworks)
	}
	sharedNet := asMap(t, topNetworks[SharedNetworkKey])
	if sharedNet["name"] != "shop_shared" || sharedNet["external"] != true {
		t.Fatalf("shared network = %#v", sharedNet)
	}
	labels := asStringMap(t, postgres["labels"])
	if labels[LabelTier] != "infra" || labels[LabelApp] != "shop" || labels[LabelProject] != "shop-infra" {
		t.Fatalf("labels = %#v", labels)
	}
	if labels[LabelSlug] != "shop_main" {
		t.Fatalf("infra slug label = %q, want main slug", labels[LabelSlug])
	}
	envFile := asSlice(t, postgres["env_file"])
	if envFile[len(envFile)-1] != ".env.infra" {
		t.Fatalf("infra env_file = %#v, want worktree-invariant .env.infra last", envFile)
	}
}

func TestRenderWorktreeProjectionRewritesSharedEdgesAndEnv(t *testing.T) {
	model := loadFixture(t, "config-basic.json")
	opts := Options{
		App:              "shop",
		Slug:             "feature",
		Project:          "shop-feature",
		SharedNetwork:    "shop_shared",
		Shared:           []string{"postgres"},
		GeneratedEnvFile: ".env.worktree",
		Services: map[string]ServiceOptions{
			"mailhog": {Autostart: false, AutostartExplicit: true},
		},
	}
	setStatefulModes(t, &opts, map[string]string{"postgres": "isolated"})

	out, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)

	services := asMap(t, doc["services"])
	if _, ok := services["postgres"]; ok {
		t.Fatalf("worktree projection included shared postgres")
	}
	api := asMap(t, services["api"])
	if _, ok := api["container_name"]; ok {
		t.Fatalf("container_name should be stripped from worktree service: %#v", api)
	}
	dependsOn := asMap(t, api["depends_on"])
	if _, ok := dependsOn["postgres"]; ok {
		t.Fatalf("shared depends_on edge was not removed: %#v", dependsOn)
	}
	if _, ok := dependsOn["worker"]; !ok {
		t.Fatalf("non-shared depends_on edge missing: %#v", dependsOn)
	}
	uplinkEdge := asMap(t, dependsOn["dt-uplink-feature"])
	if uplinkEdge["condition"] != "service_healthy" {
		t.Fatalf("shared edge should be rewritten to a healthy-uplink edge: %#v", dependsOn)
	}
	envFile := asSlice(t, api["env_file"])
	if envFile[len(envFile)-1] != ".env.worktree" {
		t.Fatalf("env_file = %#v, want .env.worktree last", envFile)
	}
	networks := keys(asMap(t, api["networks"]))
	if !contains(networks, "default") || contains(networks, SharedNetworkKey) {
		t.Fatalf("api networks = %#v, want default only (uplink owns shared-net access)", networks)
	}
	sharedNet := asMap(t, asMap(t, doc["networks"])[SharedNetworkKey])
	if sharedNet["name"] != "shop_shared" || sharedNet["external"] != true {
		t.Fatalf("shared network = %#v", sharedNet)
	}
	labels := asStringMap(t, api["labels"])
	if labels[LabelTier] != "worktree" || labels[LabelSlug] != "feature" || labels[LabelService] != "api" {
		t.Fatalf("labels = %#v", labels)
	}
	if labels[LabelData] != "isolated" {
		t.Fatalf("logical data mode label = %q, want isolated", labels[LabelData])
	}

	mailhog := asMap(t, services["mailhog"])
	profiles := asSlice(t, mailhog["profiles"])
	if len(profiles) != 1 || profiles[0] != ManualProfile {
		t.Fatalf("autostart=false profiles = %#v", profiles)
	}
}

func TestRepresentativeProjectionAndValidationRegression(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"image": "example/api:latest",
				"depends_on": {
					"postgres": {"condition": "service_healthy", "required": true},
					"redis": {"condition": "service_started", "required": true}
				},
				"env_file": [".env"],
				"networks": {"appnet": null},
				"ports": [{"target": 3000, "published": "${API_PORT:-3000}", "protocol": "tcp"}]
			},
			"frontend": {
				"image": "example/frontend:latest",
				"depends_on": ["api"],
				"ports": [{"target": 5173, "published": "5173", "protocol": "tcp"}]
			},
			"e2e": {
				"image": "example/e2e:latest",
				"depends_on": ["api"],
				"profiles": ["tools"]
			},
			"postgres": {
				"image": "postgres:16",
				"ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}", "protocol": "tcp"}],
				"volumes": [{"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"}]
			},
			"redis": {
				"image": "redis:7",
				"ports": [{"target": 6379, "published": "${REDIS_PORT:-6379}", "protocol": "tcp"}],
				"volumes": [{"type": "volume", "source": "redisdata", "target": "/data"}]
			}
		},
		"networks": {
			"appnet": {"driver": "bridge"}
		},
		"volumes": {
			"pgdata": {},
			"redisdata": {}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App:              "shop",
		Slug:             "feature_one",
		Project:          "shop-feature_one",
		InfraProject:     "shop-infra",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
		Shared:           []string{"postgres", "redis"},
		Services: map[string]ServiceOptions{
			"api":      {HostPortEnv: "API_PORT"},
			"postgres": {HostPortEnv: "POSTGRES_PORT"},
			"redis":    {HostPortEnv: "REDIS_PORT"},
			"e2e":      {Autostart: false, AutostartExplicit: true},
		},
	}

	issues := Validate(model, opts)
	if !hasIssue(issues, Warning, "hardcoded_host_port", "frontend", "5173") {
		t.Fatalf("missing hardcoded frontend port warning in %#v", issues)
	}

	infraYAML, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	infraDoc := decodeYAML(t, infraYAML)
	infraServices := asMap(t, infraDoc["services"])
	if len(infraServices) != 2 {
		t.Fatalf("infra services = %#v, want postgres and redis only", infraServices)
	}
	for _, name := range []string{"postgres", "redis"} {
		if _, ok := infraServices[name]; !ok {
			t.Fatalf("infra projection missing %s: %#v", name, infraServices)
		}
	}
	infraVolumes := asMap(t, infraDoc["volumes"])
	if len(infraVolumes) != 2 || infraVolumes["pgdata"] == nil || infraVolumes["redisdata"] == nil {
		t.Fatalf("infra volumes = %#v, want pgdata and redisdata", infraVolumes)
	}

	worktreeYAML, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDoc := decodeYAML(t, worktreeYAML)
	worktreeServices := asMap(t, worktreeDoc["services"])
	if len(worktreeServices) != 4 {
		t.Fatalf("worktree services = %#v, want api, frontend, e2e, and the uplink", worktreeServices)
	}
	if _, ok := worktreeServices["dt-uplink-feature_one"]; !ok {
		t.Fatalf("worktree services missing uplink ambassador: %#v", keys(worktreeServices))
	}
	for _, shared := range []string{"postgres", "redis"} {
		if _, ok := worktreeServices[shared]; ok {
			t.Fatalf("worktree projection included shared service %s: %#v", shared, worktreeServices)
		}
	}
	api := asMap(t, worktreeServices["api"])
	apiDependsOn := asMap(t, api["depends_on"])
	if len(apiDependsOn) != 1 {
		t.Fatalf("api depends_on = %#v, want only the uplink edge", apiDependsOn)
	}
	if asMap(t, apiDependsOn["dt-uplink-feature_one"])["condition"] != "service_healthy" {
		t.Fatalf("api shared edges should collapse into one healthy-uplink edge: %#v", apiDependsOn)
	}
	frontendDependsOn := asSlice(t, asMap(t, worktreeServices["frontend"])["depends_on"])
	if len(frontendDependsOn) != 1 || frontendDependsOn[0] != "api" {
		t.Fatalf("frontend depends_on = %#v, want api edge kept", frontendDependsOn)
	}
	envFile := asSlice(t, api["env_file"])
	if envFile[len(envFile)-1] != ".env.worktree" {
		t.Fatalf("api env_file = %#v, want .env.worktree last", envFile)
	}
	apiNetworks := keys(asMap(t, api["networks"]))
	if !contains(apiNetworks, "appnet") || !contains(apiNetworks, "default") || contains(apiNetworks, SharedNetworkKey) {
		t.Fatalf("api networks = %#v, want appnet+default only (uplink owns shared-net access)", apiNetworks)
	}
	e2eProfiles := asSlice(t, asMap(t, worktreeServices["e2e"])["profiles"])
	if !containsAny(e2eProfiles, ManualProfile) {
		t.Fatalf("e2e profiles = %#v, want %s", e2eProfiles, ManualProfile)
	}
}

func TestValidateWarnsForMixedTokenizedAndHardcodedPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"ports": [
					{"target": 3000, "published": "${API_PORT:-3000}"},
					{"target": 9229, "published": "9229"}
				]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	issues := Validate(model, Options{
		Services: map[string]ServiceOptions{
			"api": {HostPortEnv: "API_PORT"},
		},
	})
	if !hasIssue(issues, Warning, "hardcoded_host_port", "api", "9229") {
		t.Fatalf("missing hardcoded port warning for mixed api ports in %#v", issues)
	}
	if hasIssue(issues, Warning, "hardcoded_host_port", "api", "${API_PORT:-3000}") {
		t.Fatalf("tokenized api port should not be reported as hardcoded: %#v", issues)
	}
}

func TestRenderWorktreeProjectionIncludesForkedSharedServiceWithoutSharedAlias(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"depends_on": ["postgres"]
			},
			"postgres": {
				"image": "postgres:16",
				"volumes": ["pgdata:/var/lib/postgresql/data"]
			}
		},
		"volumes": {"pgdata": {}}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := RenderWorktree(model, Options{
		App:              "shop",
		Slug:             "feature_one",
		Project:          "shop-feature_one",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
		Shared:           []string{"postgres"},
		Forked: map[string]ForkOptions{
			"postgres": {VolumeName: "shop-feature_one-postgresdata"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	postgres := asMap(t, asMap(t, doc["services"])["postgres"])
	networks := keys(asMap(t, postgres["networks"]))
	if !contains(networks, "default") || contains(networks, SharedNetworkKey) {
		t.Fatalf("forked postgres networks = %#v, want default only", networks)
	}
	api := asMap(t, asMap(t, doc["services"])["api"])
	apiNetworks := keys(asMap(t, api["networks"]))
	if contains(apiNetworks, SharedNetworkKey) {
		t.Fatalf("consumer should not stay on shared network while postgres is forked: %#v", apiNetworks)
	}
	volumes := asSlice(t, postgres["volumes"])
	if len(volumes) != 1 || volumes[0] != "shop-feature_one-postgresdata:/var/lib/postgresql/data" {
		t.Fatalf("forked postgres volumes = %#v", volumes)
	}
	labels := asStringMap(t, postgres["labels"])
	if labels[LabelFork] != "postgres" || labels[LabelTier] != "worktree" {
		t.Fatalf("forked postgres labels = %#v", labels)
	}
	topVolumes := asMap(t, doc["volumes"])
	forked := asMap(t, topVolumes["shop-feature_one-postgresdata"])
	if forked["name"] != "shop-feature_one-postgresdata" || forked["external"] != true {
		t.Fatalf("forked top-level volume = %#v", forked)
	}
	if _, ok := asMap(t, doc["services"])["dt-uplink-feature_one"]; ok {
		t.Fatalf("all shared services forked: no uplink should be rendered")
	}
	if _, ok := asMap(t, doc["networks"])[SharedNetworkKey]; ok {
		t.Fatalf("all shared services forked: projection must not reference the shared network")
	}
}

func TestRenderWorktreeForkDropsOnlyTheForkedServiceFromUplink(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {"image": "example/api", "depends_on": ["postgres", "redis"]},
			"postgres": {
				"image": "postgres:16",
				"ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}],
				"volumes": ["pgdata:/var/lib/postgresql/data"]
			},
			"redis": {"image": "redis:7", "ports": [{"target": 6379, "published": "6379", "protocol": "tcp"}]}
		},
		"volumes": {"pgdata": {}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres", "redis"},
		Forked: map[string]ForkOptions{
			"postgres": {VolumeName: "shop-feature_x-postgresdata"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	services := asMap(t, doc["services"])

	// The forked postgres runs locally and carries the canonical name itself;
	// shared redis keeps its uplink — forking one service must not sever the rest.
	uplink := asMap(t, services["dt-uplink-feature_x"])
	aliases := asSlice(t, asMap(t, asMap(t, uplink["networks"])["default"])["aliases"])
	if len(aliases) != 1 || aliases[0] != "redis" {
		t.Fatalf("uplink aliases = %#v, want [redis] only", aliases)
	}
	script := asSlice(t, uplink["entrypoint"])[2].(string)
	if strings.Contains(script, "dt-upstream-postgres") || !strings.Contains(script, "dt-upstream-redis:6379") {
		t.Fatalf("uplink script should forward redis only:\n%s", script)
	}

	api := asMap(t, services["api"])
	dependsOn := asMap(t, api["depends_on"])
	if _, ok := dependsOn["postgres"]; !ok {
		t.Fatalf("edge to forked local postgres must survive: %#v", dependsOn)
	}
	if asMap(t, dependsOn["dt-uplink-feature_x"])["condition"] != "service_healthy" {
		t.Fatalf("redis edge should be rewritten to the uplink: %#v", dependsOn)
	}
	if _, ok := dependsOn["redis"]; ok {
		t.Fatalf("shared redis edge should not survive rewrite: %#v", dependsOn)
	}

	postgres := asMap(t, services["postgres"])
	postgresNetworks := keys(asMap(t, postgres["networks"]))
	if !contains(postgresNetworks, "default") || contains(postgresNetworks, SharedNetworkKey) {
		t.Fatalf("forked postgres networks = %#v, want default only", postgresNetworks)
	}
}

func TestRenderProjectionPreservesNeededConfigsAndSecrets(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"configs": [{"source": "api_config", "target": "/etc/api.yml"}],
				"secrets": ["api_token"]
			},
			"postgres": {
				"ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}],
				"configs": ["postgres_config"],
				"secrets": [{"source": "postgres_password", "target": "/run/secrets/postgres_password"}]
			}
		},
		"configs": {
			"api_config": {"file": "./api.yml"},
			"postgres_config": {"file": "./postgres.yml"},
			"unused_config": {"file": "./unused.yml"}
		},
		"secrets": {
			"api_token": {"file": "./api.token"},
			"postgres_password": {"file": "./postgres.password"},
			"unused_secret": {"file": "./unused.secret"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App:              "shop",
		Slug:             "feature_one",
		Project:          "shop-feature_one",
		InfraProject:     "shop-infra",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
		Shared:           []string{"postgres"},
	}

	worktreeYAML, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDoc := decodeYAML(t, worktreeYAML)
	worktreeConfigs := asMap(t, worktreeDoc["configs"])
	if _, ok := worktreeConfigs["api_config"]; !ok || len(worktreeConfigs) != 1 {
		t.Fatalf("worktree configs = %#v, want only api_config", worktreeConfigs)
	}
	worktreeSecrets := asMap(t, worktreeDoc["secrets"])
	if _, ok := worktreeSecrets["api_token"]; !ok || len(worktreeSecrets) != 1 {
		t.Fatalf("worktree secrets = %#v, want only api_token", worktreeSecrets)
	}

	infraYAML, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	infraDoc := decodeYAML(t, infraYAML)
	infraConfigs := asMap(t, infraDoc["configs"])
	if _, ok := infraConfigs["postgres_config"]; !ok || len(infraConfigs) != 1 {
		t.Fatalf("infra configs = %#v, want only postgres_config", infraConfigs)
	}
	infraSecrets := asMap(t, infraDoc["secrets"])
	if _, ok := infraSecrets["postgres_password"]; !ok || len(infraSecrets) != 1 {
		t.Fatalf("infra secrets = %#v, want only postgres_password", infraSecrets)
	}
}

func TestRenderProjectionPreservesBuildSecrets(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"build": {
					"context": ".",
					"dockerfile": "Dockerfile.api",
					"secrets": ["github_token", {"source": "deploy_key", "target": "deploy_key"}]
				},
				"ports": [{"target": 3001, "published": "3001", "protocol": "tcp"}]
			}
		},
		"secrets": {
			"github_token": {"environment": "GITHUB_TOKEN"},
			"deploy_key": {"file": "./deploy.key"},
			"unused_secret": {"file": "./unused.secret"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App:              "shop",
		Slug:             "feature_one",
		Project:          "shop-feature_one",
		InfraProject:     "shop-infra",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
	}

	worktreeYAML, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDoc := decodeYAML(t, worktreeYAML)
	worktreeSecrets := asMap(t, worktreeDoc["secrets"])
	if _, ok := worktreeSecrets["github_token"]; !ok {
		t.Fatalf("worktree secrets = %#v, want github_token carried for build.secrets", worktreeSecrets)
	}
	if _, ok := worktreeSecrets["deploy_key"]; !ok {
		t.Fatalf("worktree secrets = %#v, want deploy_key carried for build.secrets", worktreeSecrets)
	}
	if _, ok := worktreeSecrets["unused_secret"]; ok {
		t.Fatalf("worktree secrets = %#v, unused_secret must not be carried", worktreeSecrets)
	}
}

func setMainSlug(t *testing.T, opts *Options, slug string) {
	t.Helper()
	field := reflect.ValueOf(opts).Elem().FieldByName("MainSlug")
	if !field.IsValid() {
		t.Fatal("compose.Options is missing MainSlug")
	}
	field.SetString(slug)
}

func setStatefulModes(t *testing.T, opts *Options, modes map[string]string) {
	t.Helper()
	field := reflect.ValueOf(opts).Elem().FieldByName("StatefulModes")
	if !field.IsValid() {
		t.Fatal("compose.Options is missing StatefulModes")
	}
	field.Set(reflect.ValueOf(modes))
}

func decodeYAML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("invalid yaml:\n%s\n%v", data, err)
	}
	return doc
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value %#v is %T, want map[string]any", value, value)
	}
	return out
}

func asStringMap(t *testing.T, value any) map[string]string {
	t.Helper()
	raw := asMap(t, value)
	out := map[string]string{}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("value %#v for %s is %T, want string", v, k, v)
		}
		out[k] = s
	}
	return out
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()
	out, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is %T, want []any", value, value)
	}
	return out
}

func TestRenderWorktreeEmitsProxyLabelsAndDropsPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"image": "example/api",
				"ports": [{"target": 3000, "published": "${API_PORT:-3000}", "protocol": "tcp"}]
			},
			"worker": {
				"image": "example/worker",
				"ports": [{"target": 5555, "published": "${WORKER_PORT:-5555}", "protocol": "tcp"}]
			},
			"postgres": {"image": "postgres:16", "ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App:              "shop",
		Slug:             "feature_x",
		Project:          "shop-feature_x",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
		Shared:           []string{"postgres"},
		Proxy:            ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
	}

	out, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	services := asMap(t, doc["services"])

	api := asMap(t, services["api"])
	apiLabels := asStringMap(t, api["labels"])
	if apiLabels["caddy"] != "api-feature-x.shop.localhost" {
		t.Fatalf("api caddy label = %q", apiLabels["caddy"])
	}
	if apiLabels["caddy.reverse_proxy"] != "{{upstreams 3000}}" {
		t.Fatalf("api caddy.reverse_proxy = %q", apiLabels["caddy.reverse_proxy"])
	}
	if _, ok := api["ports"]; ok {
		t.Fatalf("proxied api should not publish host ports: %#v", api["ports"])
	}
	// Proxied worktree services join a PER-PROJECT proxy network, not a single
	// machine-global one — otherwise same-named services across apps/worktrees
	// (e.g. every app's "api") collide as DNS aliases on the shared network.
	if !contains(keys(asMap(t, api["networks"])), "shop-feature_x_proxy") {
		t.Fatalf("api networks = %#v, want shop-feature_x_proxy", api["networks"])
	}
	if contains(keys(asMap(t, api["networks"])), ProxyNetworkKey) {
		t.Fatalf("api must not join the machine-global proxy network: %#v", api["networks"])
	}

	worker := asMap(t, services["worker"])
	if _, ok := asStringMap(t, worker["labels"])["caddy"]; ok {
		t.Fatalf("non-HTTP worker should not be proxied")
	}
	if _, ok := worker["ports"]; !ok {
		t.Fatalf("non-proxied worker should keep its ports")
	}

	proxyNet := asMap(t, asMap(t, doc["networks"])["shop-feature_x_proxy"])
	if proxyNet["name"] != "shop-feature_x_proxy" || proxyNet["external"] != true {
		t.Fatalf("proxy network = %#v", proxyNet)
	}
}

func TestRenderWorktreeProxyDisabledKeepsPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {"api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := asMap(t, asMap(t, decodeYAML(t, out)["services"])["api"])
	if _, ok := asStringMap(t, api["labels"])["caddy"]; ok {
		t.Fatalf("proxy disabled should emit no caddy label")
	}
	if _, ok := api["ports"]; !ok {
		t.Fatalf("proxy disabled should keep ports")
	}
}

func TestRenderWorktreeExposeOverrides(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api":    {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
			"mailer": {"ports": [{"target": 2525, "published": "${MAIL_PORT:-2525}"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
		Services: map[string]ServiceOptions{
			"api":    {Expose: "none"},
			"mailer": {Expose: "http", ProxyPort: 2525},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := asMap(t, decodeYAML(t, out)["services"])
	if _, ok := asStringMap(t, asMap(t, services["api"])["labels"])["caddy"]; ok {
		t.Fatalf("api expose=none should not be proxied")
	}
	mailer := asMap(t, services["mailer"])
	mailerLabels := asStringMap(t, mailer["labels"])
	if mailerLabels["caddy"] != "mailer-feature-x.shop.localhost" {
		t.Fatalf("mailer caddy = %q", mailerLabels["caddy"])
	}
	if mailerLabels["caddy.reverse_proxy"] != "{{upstreams 2525}}" {
		t.Fatalf("mailer upstreams = %q", mailerLabels["caddy.reverse_proxy"])
	}
}

func TestRenderProxyDoc(t *testing.T) {
	out, err := RenderProxy(ProxyRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)

	svc := asMap(t, asMap(t, doc["services"])["docktree-proxy"])
	if svc["image"] != DefaultProxyImage {
		t.Fatalf("image = %v, want %s", svc["image"], DefaultProxyImage)
	}
	ports := asSlice(t, svc["ports"])
	if len(ports) != 2 || ports[0] != "80:80" || ports[1] != "443:443" {
		t.Fatalf("ports = %#v", ports)
	}
	vols := asSlice(t, svc["volumes"])
	if vols[0] != "/var/run/docker.sock:/var/run/docker.sock:ro" {
		t.Fatalf("first volume = %v, want read-only docker socket", vols[0])
	}
	if !containsAny(vols, ProxyDataVolume+":/data") {
		t.Fatalf("volumes missing CA data mount: %#v", vols)
	}
	labels := asStringMap(t, svc["labels"])
	if labels[LabelTier] != "proxy" || labels[LabelManaged] != "true" {
		t.Fatalf("proxy labels = %#v", labels)
	}
	net := asMap(t, asMap(t, doc["networks"])[ProxyNetworkKey])
	if net["name"] != ProxyNetworkKey || net["external"] != true {
		t.Fatalf("proxy network = %#v", net)
	}
	if _, ok := svc["healthcheck"]; !ok {
		t.Fatalf("proxy service missing healthcheck")
	}
}

func TestRenderProxyDocCustomPorts(t *testing.T) {
	out, err := RenderProxy(ProxyRenderOptions{HTTPPort: 8080, HTTPSPort: 8443})
	if err != nil {
		t.Fatal(err)
	}
	svc := asMap(t, asMap(t, decodeYAML(t, out)["services"])["docktree-proxy"])
	ports := asSlice(t, svc["ports"])
	if ports[0] != "8080:80" || ports[1] != "8443:443" {
		t.Fatalf("custom ports = %#v", ports)
	}
}

func TestRenderWorktreeExcludesStatefulFromProxy(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {"db": {"ports": [{"target": 8080, "published": "${DB_PORT:-8080}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
	}
	setStatefulModes(t, &opts, map[string]string{"db": "shared"})

	out, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	db := asMap(t, asMap(t, decodeYAML(t, out)["services"])["db"])
	if _, ok := asStringMap(t, db["labels"])["caddy"]; ok {
		t.Fatalf("stateful service must not be proxied even with an HTTP port")
	}
	if _, ok := db["ports"]; !ok {
		t.Fatalf("stateful service should keep its host ports")
	}
}

func TestRenderInfraProxiesExposedSharedService(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"minio": {"image": "minio/minio", "ports": [{"target": 9000, "published": "${MINIO_PORT:-9000}", "protocol": "tcp"}]},
			"postgres": {"image": "postgres:16"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App: "shop", Slug: "feature", InfraProject: "shop-infra",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"minio", "postgres"},
		Proxy:  ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
		Services: map[string]ServiceOptions{
			"minio": {Expose: "http", ProxyPort: 9000},
		},
	}
	setMainSlug(t, &opts, "shop_main")

	out, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	services := asMap(t, decodeYAML(t, out)["services"])

	minio := asMap(t, services["minio"])
	labels := asStringMap(t, minio["labels"])
	// Slug-less host even though Slug=feature: one stable host for the shared instance.
	if labels["caddy"] != "minio.shop.localhost" {
		t.Fatalf("minio caddy label = %q, want minio.shop.localhost", labels["caddy"])
	}
	if labels["caddy.reverse_proxy"] != "{{upstreams 9000}}" {
		t.Fatalf("minio caddy.reverse_proxy = %q", labels["caddy.reverse_proxy"])
	}
	if _, ok := minio["ports"]; ok {
		t.Fatalf("proxied shared minio should not publish host ports: %#v", minio["ports"])
	}
	nets := keys(asMap(t, minio["networks"]))
	if !contains(nets, SharedNetworkKey) || !contains(nets, "shop-infra_proxy") {
		t.Fatalf("minio networks = %#v, want dual-homed on shared + shop-infra_proxy", nets)
	}
	if contains(nets, ProxyNetworkKey) {
		t.Fatalf("proxied infra must not join the machine-global proxy network: %#v", nets)
	}
	minioAliases := asSlice(t, asMap(t, asMap(t, minio["networks"])[SharedNetworkKey])["aliases"])
	if len(minioAliases) != 1 || minioAliases[0] != "dt-upstream-minio" {
		t.Fatalf("proxied minio must keep its upstream alias: %#v", minioAliases)
	}

	// A shared service that did NOT opt in stays a pure data tier.
	postgres := asMap(t, services["postgres"])
	if _, ok := asStringMap(t, postgres["labels"])["caddy"]; ok {
		t.Fatalf("non-opted shared postgres must not be proxied")
	}

	proxyNet := asMap(t, asMap(t, decodeYAML(t, out)["networks"])["shop-infra_proxy"])
	if proxyNet["name"] != "shop-infra_proxy" || proxyNet["external"] != true {
		t.Fatalf("proxy network = %#v", proxyNet)
	}
}

func TestRenderInfraSharedServiceNotProxiedWithoutExposeOptIn(t *testing.T) {
	// A shared service with an HTTP-allowlist port (9000) published but no explicit
	// expose="http" must NOT be auto-proxied: the port heuristic does not apply to
	// shared services; opting in is explicit.
	model, err := ParseConfigJSON([]byte(`{
		"services": {"minio": {"image": "minio/minio", "ports": [{"target": 9000, "published": "${MINIO_PORT:-9000}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App: "shop", Slug: "feature", InfraProject: "shop-infra",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"minio"},
		Proxy:  ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
	}
	setMainSlug(t, &opts, "shop_main")

	out, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	minio := asMap(t, asMap(t, decodeYAML(t, out)["services"])["minio"])
	if _, ok := asStringMap(t, minio["labels"])["caddy"]; ok {
		t.Fatalf("shared minio without expose=http must not be proxied")
	}
	if _, ok := minio["ports"]; !ok {
		t.Fatalf("non-proxied shared minio should keep its host ports")
	}
}

func TestRenderInfraSharedStatefulServiceNotProxiedEvenIfExposed(t *testing.T) {
	// postgres is shared AND stateful. Even with expose="http" it must stay
	// unproxied — clients dial it as a database, not over HTTP.
	model, err := ParseConfigJSON([]byte(`{
		"services": {"postgres": {"image": "postgres:16", "ports": [{"target": 8080, "published": "${PG_PORT:-8080}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App: "shop", Slug: "feature", InfraProject: "shop-infra",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres"},
		Proxy:  ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
		Services: map[string]ServiceOptions{
			"postgres": {Expose: "http"},
		},
	}
	setMainSlug(t, &opts, "shop_main")
	setStatefulModes(t, &opts, map[string]string{"postgres": "shared"})

	out, err := RenderInfra(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	postgres := asMap(t, asMap(t, decodeYAML(t, out)["services"])["postgres"])
	if _, ok := asStringMap(t, postgres["labels"])["caddy"]; ok {
		t.Fatalf("shared+stateful postgres must not be proxied even with expose=http")
	}
	if _, ok := postgres["ports"]; !ok {
		t.Fatalf("shared+stateful postgres should keep its host ports")
	}
}

func TestRenderWorktreeProxyUpstreamFallbacks(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"mailer": {"ports": [{"target": 2525, "published": "${MAIL_PORT:-2525}"}]},
			"ghost": {}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
		Services: map[string]ServiceOptions{
			"mailer": {Expose: "http"},
			"ghost":  {Expose: "http"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := asMap(t, decodeYAML(t, out)["services"])
	if got := asStringMap(t, asMap(t, services["mailer"])["labels"])["caddy.reverse_proxy"]; got != "{{upstreams 2525}}" {
		t.Fatalf("mailer upstream = %q, want first-port fallback 2525", got)
	}
	if got := asStringMap(t, asMap(t, services["ghost"])["labels"])["caddy.reverse_proxy"]; got != "{{upstreams 80}}" {
		t.Fatalf("ghost upstream = %q, want default 80 (no unroutable upstreams 0)", got)
	}
}

func TestRenderProxyDocCustomImageAndNetwork(t *testing.T) {
	// Caddy fronts every app, so it must attach to (and treat as ingress) every
	// per-project proxy network — passed in as a list and joined into
	// CADDY_INGRESS_NETWORKS so backends on any of them stay eligible.
	out, err := RenderProxy(ProxyRenderOptions{Image: "myreg/caddy:custom", Networks: []string{"alt_net", "other_net"}})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	svc := asMap(t, asMap(t, doc["services"])["docktree-proxy"])
	if svc["image"] != "myreg/caddy:custom" {
		t.Fatalf("image = %v", svc["image"])
	}
	if asStringMap(t, svc["environment"])["CADDY_INGRESS_NETWORKS"] != "alt_net,other_net" {
		t.Fatalf("ingress network env = %#v", svc["environment"])
	}
	svcNets := asSlice(t, svc["networks"])
	if !containsAny(svcNets, "alt_net") || !containsAny(svcNets, "other_net") {
		t.Fatalf("caddy must attach to every ingress network: %#v", svcNets)
	}
	for _, n := range []string{"alt_net", "other_net"} {
		net := asMap(t, asMap(t, doc["networks"])[n])
		if net["name"] != n || net["external"] != true {
			t.Fatalf("network %q = %#v, want external", n, net)
		}
	}
}

func TestProxyHostSuffixDefaulting(t *testing.T) {
	opts := Options{App: "shop", Slug: "feature_x", Proxy: ProxyOptions{}}
	if got := ProxyHost(opts, "api"); got != "api-feature-x.shop.localhost" {
		t.Fatalf("ProxyHost default suffix = %q", got)
	}
	opts.Proxy.DNSSuffix = "127.0.0.1.sslip.io"
	if got := ProxyHost(opts, "api"); got != "api-feature-x.shop.127.0.0.1.sslip.io" {
		t.Fatalf("ProxyHost custom suffix = %q", got)
	}
}

func TestProxyHostCollapsesMainWorktreeSlug(t *testing.T) {
	// In the primary worktree the slug is derived from the repo directory, so it
	// equals the app. The redundant slug suffix must be dropped (mirroring the
	// project-name collapse) rather than emitting <service>-<app>.<app>.
	opts := Options{App: "ts_fullstack_app", Slug: "ts_fullstack_app", Proxy: ProxyOptions{}}
	if got := ProxyHost(opts, "web"); got != "web.ts-fullstack-app.localhost" {
		t.Fatalf("ProxyHost main worktree = %q, want web.ts-fullstack-app.localhost", got)
	}
	// A linked worktree carries its slug as a hyphen suffix in the leftmost label,
	// so a single WorkOS wildcard (web-*.ts-fullstack-app.localhost) covers them.
	opts.Slug = "feature_x"
	if got := ProxyHost(opts, "web"); got != "web-feature-x.ts-fullstack-app.localhost" {
		t.Fatalf("ProxyHost linked worktree = %q, want web-feature-x.ts-fullstack-app.localhost", got)
	}
}

func TestProxyURLOmitsDefaultPortAndIncludesCustom(t *testing.T) {
	opts := Options{App: "shop", Slug: "shop", Shared: []string{"minio"}, Proxy: ProxyOptions{Enabled: true, DNSSuffix: "localhost", HTTPSPort: 443}}
	if got := ProxyURL(opts, "minio"); got != "https://minio.shop.localhost" {
		t.Fatalf("ProxyURL default port = %q, want no port", got)
	}
	opts.Proxy.HTTPSPort = 8443
	if got := ProxyURL(opts, "minio"); got != "https://minio.shop.localhost:8443" {
		t.Fatalf("ProxyURL custom port = %q, want :8443", got)
	}
}

func TestProxyHostSlugLessForSharedService(t *testing.T) {
	// A shared service has exactly one instance per app (not per worktree), so its
	// proxy hostname must NOT carry the worktree slug — every worktree must derive
	// the same stable host (e.g. minio.shop.localhost), or they would fight over
	// the single infra container's caddy label.
	opts := Options{App: "shop", Slug: "feature_x", Shared: []string{"minio"}, Proxy: ProxyOptions{}}
	if got := ProxyHost(opts, "minio"); got != "minio.shop.localhost" {
		t.Fatalf("shared ProxyHost = %q, want slug-less minio.shop.localhost", got)
	}
	// A non-shared (worktree) service still carries the slug suffix.
	if got := ProxyHost(opts, "api"); got != "api-feature-x.shop.localhost" {
		t.Fatalf("worktree ProxyHost = %q, want api-feature-x.shop.localhost", got)
	}
}

func TestRenderWorktreeInjectsUplinkAmbassador(t *testing.T) {
	model := loadFixture(t, "config-basic.json")
	opts := Options{
		App:              "shop",
		Slug:             "feature",
		Project:          "shop-feature",
		SharedNetwork:    "shop_shared",
		Shared:           []string{"postgres"},
		GeneratedEnvFile: ".env.worktree",
	}

	out, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	services := asMap(t, doc["services"])

	uplink := asMap(t, services["dt-uplink-feature"])
	if uplink["image"] != DefaultUplinkImage {
		t.Fatalf("uplink image = %v, want %s", uplink["image"], DefaultUplinkImage)
	}
	if uplink["restart"] != "unless-stopped" {
		t.Fatalf("uplink restart = %v", uplink["restart"])
	}
	networks := asMap(t, uplink["networks"])
	defaultNet := asMap(t, networks["default"])
	aliases := asSlice(t, defaultNet["aliases"])
	if len(aliases) != 1 || aliases[0] != "postgres" {
		t.Fatalf("uplink default-net aliases = %#v, want [postgres]", aliases)
	}
	if _, ok := networks[SharedNetworkKey]; !ok {
		t.Fatalf("uplink networks = %#v, want shared-net membership", keys(networks))
	}
	labels := asStringMap(t, uplink["labels"])
	if labels[LabelTier] != "uplink" || labels[LabelSlug] != "feature" || labels[LabelApp] != "shop" {
		t.Fatalf("uplink labels = %#v", labels)
	}
	if _, ok := uplink["env_file"]; ok {
		t.Fatalf("uplink must not load env files: %#v", uplink["env_file"])
	}

	sharedNet := asMap(t, asMap(t, doc["networks"])[SharedNetworkKey])
	if sharedNet["name"] != "shop_shared" || sharedNet["external"] != true {
		t.Fatalf("shared network = %#v", sharedNet)
	}
}

func TestRenderWorktreeUplinkForwardsAndProbesUpstreamPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {"image": "example/api"},
			"postgres": {"image": "postgres:16", "ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}", "protocol": "tcp"}]},
			"redis": {"image": "redis:7", "ports": [{"target": 6379, "published": "${REDIS_PORT:-6379}", "protocol": "tcp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"redis", "postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	uplink := asMap(t, asMap(t, decodeYAML(t, out)["services"])["dt-uplink-feature_x"])

	aliases := asSlice(t, asMap(t, asMap(t, uplink["networks"])["default"])["aliases"])
	if len(aliases) != 2 || aliases[0] != "postgres" || aliases[1] != "redis" {
		t.Fatalf("uplink aliases = %#v, want sorted [postgres redis]", aliases)
	}

	entrypoint := asSlice(t, uplink["entrypoint"])
	if len(entrypoint) != 3 || entrypoint[0] != "/bin/sh" || entrypoint[1] != "-ec" {
		t.Fatalf("uplink entrypoint = %#v", entrypoint)
	}
	script, ok := entrypoint[2].(string)
	if !ok {
		t.Fatalf("uplink script = %#v", entrypoint[2])
	}
	for _, want := range []string{
		"socat TCP-LISTEN:5432,fork,reuseaddr TCP:dt-upstream-postgres:5432 &",
		"socat TCP-LISTEN:6379,fork,reuseaddr TCP:dt-upstream-redis:6379 &",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("uplink script missing %q:\n%s", want, script)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(script), "wait") {
		t.Fatalf("uplink script must end in wait:\n%s", script)
	}

	health := asMap(t, uplink["healthcheck"])
	test := asSlice(t, health["test"])
	if len(test) != 2 || test[0] != "CMD-SHELL" {
		t.Fatalf("uplink healthcheck test = %#v", test)
	}
	probe, ok := test[1].(string)
	if !ok || !strings.Contains(probe, "socat -T 2 /dev/null TCP:dt-upstream-postgres:5432") ||
		!strings.Contains(probe, "socat -T 2 /dev/null TCP:dt-upstream-redis:6379") ||
		!strings.Contains(probe, " && ") {
		t.Fatalf("uplink health probe = %#v", test[1])
	}
}

func TestRenderWorktreeRewritesListFormSharedDependsOn(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"worker": {"image": "example/worker", "depends_on": ["postgres", "cache"]},
			"cache": {"image": "redis:7"},
			"postgres": {"image": "postgres:16", "ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := asMap(t, asMap(t, decodeYAML(t, out)["services"])["worker"])
	dependsOn := asMap(t, worker["depends_on"])
	if asMap(t, dependsOn["dt-uplink-feature_x"])["condition"] != "service_healthy" {
		t.Fatalf("list-form shared edge not rewritten to uplink: %#v", dependsOn)
	}
	if asMap(t, dependsOn["cache"])["condition"] != "service_started" {
		t.Fatalf("list-form non-shared edge should keep started semantics: %#v", dependsOn)
	}
	if _, ok := dependsOn["postgres"]; ok {
		t.Fatalf("shared edge should not survive rewrite: %#v", dependsOn)
	}
}

func TestRenderWorktreeSplitsUplinkOnPortCollision(t *testing.T) {
	// Two shared services on the same container port cannot share one
	// ambassador (one IP, one listener); the collider gets its own.
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {"image": "example/api", "depends_on": ["registry"]},
			"minio": {"image": "minio/minio", "ports": [{"target": 9000, "published": "9000", "protocol": "tcp"}]},
			"registry": {"image": "registry:2", "ports": [{"target": 9000, "published": "9001", "protocol": "tcp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"minio", "registry"},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := asMap(t, decodeYAML(t, out)["services"])

	primary := asMap(t, services["dt-uplink-feature_x"])
	primaryAliases := asSlice(t, asMap(t, asMap(t, primary["networks"])["default"])["aliases"])
	if len(primaryAliases) != 1 || primaryAliases[0] != "minio" {
		t.Fatalf("primary uplink aliases = %#v, want [minio]", primaryAliases)
	}

	// Dedicated name extends the slug-unique primary with a dot segment, so it
	// can never equal another worktree's primary or dedicated uplink name.
	dedicated := asMap(t, services["dt-uplink-feature_x.registry"])
	dedicatedAliases := asSlice(t, asMap(t, asMap(t, dedicated["networks"])["default"])["aliases"])
	if len(dedicatedAliases) != 1 || dedicatedAliases[0] != "registry" {
		t.Fatalf("dedicated uplink aliases = %#v, want [registry]", dedicatedAliases)
	}
	script := asSlice(t, dedicated["entrypoint"])[2].(string)
	if !strings.Contains(script, "socat TCP-LISTEN:9000,fork,reuseaddr TCP:dt-upstream-registry:9000 &") {
		t.Fatalf("dedicated uplink script = %s", script)
	}

	api := asMap(t, services["api"])
	dependsOn := asMap(t, api["depends_on"])
	if asMap(t, dependsOn["dt-uplink-feature_x.registry"])["condition"] != "service_healthy" {
		t.Fatalf("registry edge should point at its dedicated uplink: %#v", dependsOn)
	}
}

func TestRenderRejectsReservedUplinkServiceName(t *testing.T) {
	for _, name := range []string{"dt-uplink", "dt-uplink-custom", "dt-uplink.x"} {
		model, err := ParseConfigJSON([]byte(`{
			"services": {"` + name + `": {"image": "example/x"}}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = RenderWorktree(model, Options{
			App: "shop", Slug: "feature_x", Project: "shop-feature_x",
			SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("name %q: err = %v, want reserved-prefix rejection", name, err)
		}
	}

	// Names that merely share the prefix string are not in the reserved
	// namespace (generated names are always dt-uplink-<slug>[.<svc>]).
	model, err := ParseConfigJSON([]byte(`{
		"services": {"dt-uplinker": {"image": "example/x"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
	}); err != nil {
		t.Fatalf("dt-uplinker should not be rejected: %v", err)
	}
}

func TestRenderErrorsForSharedServiceMissingFromModel(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {"api": {"image": "example/api"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"ghost"},
	})
	if err == nil || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("err = %v, want actionable missing-service error naming ghost", err)
	}
}

func TestRenderWorktreeUplinkForwardsAllPortsAndProbesListeners(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"minio": {
				"image": "minio/minio",
				"ports": [
					{"target": 9000, "published": "9000", "protocol": "tcp"},
					{"target": 9001, "published": "9001", "protocol": "tcp"}
				]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"minio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	uplink := asMap(t, asMap(t, decodeYAML(t, out)["services"])["dt-uplink-feature_x"])
	script := asSlice(t, uplink["entrypoint"])[2].(string)
	for _, want := range []string{
		"socat TCP-LISTEN:9000,fork,reuseaddr TCP:dt-upstream-minio:9000 &",
		"socat TCP-LISTEN:9001,fork,reuseaddr TCP:dt-upstream-minio:9001 &",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("uplink script missing %q:\n%s", want, script)
		}
	}
	health := asMap(t, uplink["healthcheck"])
	probe := asSlice(t, health["test"])[1].(string)
	for _, want := range []string{
		// Upstream probes: infra reachable.
		"socat -T 2 /dev/null TCP:dt-upstream-minio:9000",
		"socat -T 2 /dev/null TCP:dt-upstream-minio:9001",
		// Listener probes: a dead forwarder must turn the container unhealthy.
		"socat -T 2 /dev/null TCP:127.0.0.1:9000",
		"socat -T 2 /dev/null TCP:127.0.0.1:9001",
	} {
		if !strings.Contains(probe, want) {
			t.Fatalf("health probe missing %q: %s", want, probe)
		}
	}
	// 4 probes x 2s budget + 2s slack.
	if health["timeout"] != "10s" {
		t.Fatalf("health timeout = %v, want 10s", health["timeout"])
	}
}

func TestRenderWorktreeUplinkErrorsForUDPOnlySharedService(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"dns": {"image": "example/dns", "ports": [{"target": 53, "published": "53", "protocol": "udp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"dns"},
	})
	if err == nil || !strings.Contains(err.Error(), "no TCP ports") {
		t.Fatalf("err = %v, want no-TCP-ports error for udp-only shared service", err)
	}
}

func TestRenderWorktreeUplinkParsesExposeProtocolAndRanges(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"nats": {"image": "nats:2", "expose": ["4222/tcp", "9000-9002", "53/udp"]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"nats"},
	})
	if err != nil {
		t.Fatal(err)
	}
	uplink := asMap(t, asMap(t, decodeYAML(t, out)["services"])["dt-uplink-feature_x"])
	script := asSlice(t, uplink["entrypoint"])[2].(string)
	for _, port := range []string{"4222", "9000", "9001", "9002"} {
		if !strings.Contains(script, "socat TCP-LISTEN:"+port+",fork,reuseaddr TCP:dt-upstream-nats:"+port+" &") {
			t.Fatalf("uplink script missing forwarder for %s:\n%s", port, script)
		}
	}
	if strings.Contains(script, ":53") {
		t.Fatalf("udp expose entry must not be forwarded:\n%s", script)
	}
}

func TestRenderWorktreeUplinkDedupesPortsAndExpose(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"postgres": {
				"image": "postgres:16",
				"ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}],
				"expose": ["5432"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	uplink := asMap(t, asMap(t, decodeYAML(t, out)["services"])["dt-uplink-feature_x"])
	script := asSlice(t, uplink["entrypoint"])[2].(string)
	if strings.Count(script, "TCP-LISTEN:5432,") != 1 {
		t.Fatalf("port declared in both ports and expose must yield one forwarder:\n%s", script)
	}
}

func TestRenderWorktreeNoUplinkWithoutSharedServices(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {"api": {"image": "example/api"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	for name := range asMap(t, doc["services"]) {
		if strings.HasPrefix(name, UplinkServicePrefix) {
			t.Fatalf("no shared services: no uplink expected, got %s", name)
		}
	}
	if networks, ok := doc["networks"].(map[string]any); ok {
		if _, ok := networks[SharedNetworkKey]; ok {
			t.Fatalf("no shared services: projection must not reference the shared network")
		}
	}
}

func TestRenderWorktreePreservesOptionalSharedDependency(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"image": "example/api",
				"depends_on": {"postgres": {"condition": "service_started", "required": false}}
			},
			"postgres": {"image": "postgres:16", "ports": [{"target": 5432, "published": "5432", "protocol": "tcp"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := asMap(t, asMap(t, decodeYAML(t, out)["services"])["api"])
	edge := asMap(t, asMap(t, api["depends_on"])["dt-uplink-feature_x"])
	if edge["condition"] != "service_healthy" || edge["required"] != false {
		t.Fatalf("optional shared edge must stay optional on the uplink: %#v", edge)
	}
}

func TestRenderWorktreeUplinkDerivesPortsFromExpose(t *testing.T) {
	// A shared service with no published host ports still declares its
	// container port via expose; the uplink must forward it.
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {"image": "example/api"},
			"postgres": {"image": "postgres:16", "expose": ["5432"]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Shared: []string{"postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	uplink := asMap(t, asMap(t, decodeYAML(t, out)["services"])["dt-uplink-feature_x"])
	script := asSlice(t, uplink["entrypoint"])[2].(string)
	if !strings.Contains(script, "socat TCP-LISTEN:5432,fork,reuseaddr TCP:dt-upstream-postgres:5432 &") {
		t.Fatalf("uplink script missing expose-derived forwarder:\n%s", script)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
