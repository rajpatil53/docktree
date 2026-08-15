package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseConfigJSONReadsNeededComposeFields(t *testing.T) {
	model := loadFixture(t, "config-basic.json")

	if len(model.Services) != 4 {
		t.Fatalf("services = %d, want 4", len(model.Services))
	}
	api := model.Services["api"]
	if api.ContainerName != "fixed-api" {
		t.Fatalf("container_name = %q", api.ContainerName)
	}
	if !reflect.DeepEqual(api.EnvFile, []string{".env"}) {
		t.Fatalf("env_file = %#v", api.EnvFile)
	}
	if _, ok := api.DependsOn["postgres"]; !ok {
		t.Fatalf("api depends_on missing postgres: %#v", api.DependsOn)
	}
	if len(api.Ports) != 1 || api.Ports[0].Published != "3000" || api.Ports[0].Target != 3000 {
		t.Fatalf("api ports = %#v", api.Ports)
	}
	if len(api.Volumes) != 1 || api.Volumes[0].Source != "api-cache" || api.Volumes[0].Type != "volume" {
		t.Fatalf("api volumes = %#v", api.Volumes)
	}
	if _, ok := model.Networks["backplane"]; !ok {
		t.Fatalf("top-level networks = %#v", model.Networks)
	}
	if _, ok := model.Volumes["pgdata"]; !ok {
		t.Fatalf("top-level volumes = %#v", model.Volumes)
	}
}

func TestParseConfigJSONAcceptsComposeVariantShapes(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"networks": "frontend",
				"ports": ["8080:3000", "${API_PORT:-3000}:3000", "127.0.0.1:${DEBUG_PORT:-9229}:9229/tcp"],
				"volumes": ["cache:/cache", "./src:/src"],
				"configs": ["app_config", {"source": "feature_flags", "target": "/flags"}],
				"secrets": [{"target": "api_key"}],
				"labels": ["com.example.enabled=true", "com.example.empty"],
				"env_file": {"path": ".env.local"},
				"profiles": "debug"
			}
		},
		"networks": {"frontend": {}},
		"volumes": {"cache": {}},
		"configs": {"app_config": {}, "feature_flags": {}},
		"secrets": {"api_key": {}}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	api := model.Services["api"]
	if !reflect.DeepEqual(api.Networks, []string{"frontend"}) {
		t.Fatalf("networks = %#v", api.Networks)
	}
	if len(api.Ports) != 3 || api.Ports[0].Published != "8080" || api.Ports[0].Target != 3000 {
		t.Fatalf("ports = %#v", api.Ports)
	}
	if api.Ports[1].Published != "${API_PORT:-3000}" || api.Ports[1].Target != 3000 {
		t.Fatalf("tokenized short port = %#v", api.Ports[1])
	}
	if api.Ports[2].Published != "${DEBUG_PORT:-9229}" || api.Ports[2].Target != 9229 || api.Ports[2].Protocol != "tcp" {
		t.Fatalf("ip-bound tokenized short port = %#v", api.Ports[2])
	}
	if len(api.Volumes) != 1 || api.Volumes[0].Source != "cache" || api.Volumes[0].Target != "/cache" {
		t.Fatalf("volumes = %#v", api.Volumes)
	}
	if len(api.Configs) != 2 || api.Configs[1].Source != "feature_flags" {
		t.Fatalf("configs = %#v", api.Configs)
	}
	if len(api.Secrets) != 1 || api.Secrets[0].Source != "api_key" {
		t.Fatalf("secrets = %#v", api.Secrets)
	}
	if api.Labels["com.example.enabled"] != "true" {
		t.Fatalf("labels = %#v", api.Labels)
	}
	if !reflect.DeepEqual(api.EnvFile, []string{".env.local"}) {
		t.Fatalf("env_file = %#v", api.EnvFile)
	}
	if !reflect.DeepEqual(api.Profiles, []string{"debug"}) {
		t.Fatalf("profiles = %#v", api.Profiles)
	}
}

func TestValidateReportsConfiguredHostPortEnvMissingFromCompose(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {"ports": [{"target": 3000, "published": "3000"}]},
			"worker": {}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	issues := Validate(model, Options{
		Services: map[string]ServiceOptions{
			"api":    {HostPortEnv: "API_PORT"},
			"worker": {HostPortEnv: "WORKER_PORT"},
		},
	})

	if !hasIssue(issues, Error, "missing_host_port_token", "api", "API_PORT") {
		t.Fatalf("missing API_PORT diagnostic in %#v", issues)
	}
	if !hasIssue(issues, Error, "missing_host_port_token", "worker", "WORKER_PORT") {
		t.Fatalf("missing WORKER_PORT diagnostic in %#v", issues)
	}
}

func TestValidateComposeModelReportsProjectionHazards(t *testing.T) {
	model := loadFixture(t, "config-shared-depends-on.json")

	issues := Validate(model, Options{
		App:      "shop",
		Slug:     "feature",
		Shared:   []string{"postgres"},
		Services: map[string]ServiceOptions{"api": {HostPortEnv: "API_PORT"}},
	})

	if !hasIssue(issues, Error, "shared_depends_on_non_shared", "postgres", "api") {
		t.Fatalf("expected shared dependency error, got %#v", issues)
	}
}

func TestValidateComposeModelWarnsAboutPortsVolumesAndContainerNames(t *testing.T) {
	model := loadFixture(t, "config-basic.json")

	issues := Validate(model, Options{
		App:    "shop",
		Slug:   "feature",
		Shared: []string{"postgres"},
		Services: map[string]ServiceOptions{
			"api": {HostPortEnv: "API_PORT"},
		},
	})

	for _, want := range []struct {
		code     string
		service  string
		resource string
	}{
		{"hardcoded_host_port", "postgres", "5432"},
		{"hardcoded_host_port", "mailhog", "8025"},
		{"container_name", "api", "fixed-api"},
		{"container_name", "postgres", "fixed-postgres"},
		{"named_volume", "", "named-data"},
		{"external_volume", "", "external-data"},
	} {
		if !hasIssue(issues, Warning, want.code, want.service, want.resource) {
			t.Fatalf("missing warning %s/%s/%s in %#v", want.code, want.service, want.resource, issues)
		}
	}
}

func TestDiscoverFilesHonorsComposeFileEnv(t *testing.T) {
	wd := t.TempDir()

	files, err := DiscoverFiles(wd, map[string]string{
		"COMPOSE_FILE": "compose.yml" + string(filepath.ListSeparator) + "compose.dev.yml",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(wd, "compose.yml"),
		filepath.Join(wd, "compose.dev.yml"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}

	argv := ConfigCommandArgv(files)
	wantArgv := []string{"docker", "compose", "-f", want[0], "-f", want[1], "config", "--no-interpolate", "--no-normalize", "--format", "json"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
	}
}

func TestDiscoverFilesIncludesDefaultDockerComposeOverride(t *testing.T) {
	wd := t.TempDir()
	for _, name := range []string{"docker-compose.yml", "docker-compose.override.yml"} {
		if err := os.WriteFile(filepath.Join(wd, name), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := DiscoverFiles(wd, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(wd, "docker-compose.yml"),
		filepath.Join(wd, "docker-compose.override.yml"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func TestDiscoverFilesUsesOnlyFirstDefaultOverride(t *testing.T) {
	wd := t.TempDir()
	for _, name := range []string{"compose.yaml", "compose.override.yaml", "docker-compose.override.yml"} {
		if err := os.WriteFile(filepath.Join(wd, name), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := DiscoverFiles(wd, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(wd, "compose.yaml"),
		filepath.Join(wd, "compose.override.yaml"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func loadFixture(t *testing.T, name string) *Model {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	model, err := ParseConfigJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func hasIssue(issues []Issue, severity Severity, code, service, resource string) bool {
	for _, issue := range issues {
		if issue.Severity == severity && issue.Code == code && issue.Service == service && issue.Resource == resource {
			return true
		}
	}
	return false
}
