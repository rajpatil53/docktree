package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/dockerstate"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
)

const diagnosticComposeConfigJSON = `{
  "services": {
    "api": {
      "ports": [{"target": 3000, "published": "4000"}]
    },
    "postgres": {
      "container_name": "fixed-postgres",
      "depends_on": {"api": {"condition": "service_started", "required": true}},
      "ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]
    }
  },
  "volumes": {
    "pgdata": {"name": "shared_pgdata"},
    "uploads": {"external": true}
  }
}`

func TestDoctorReportsDockerComposeInfraPortAndSecretsDiagnostics(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, `
app = "shop"
shared = ["postgres", "redis"]

[services.postgres]
host_port_env = "POSTGRES_PORT"

[secrets]
wrapper = "doppler run --"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(diagnosticComposeConfigJSON),
		outputErrByArgv: map[string]error{
			"docker info --format {{json .ServerVersion}}": errors.New("cannot connect"),
		},
		runErrByArgv: map[string]error{
			"doppler run -- true": errors.New("doppler missing"),
		},
		outputByArgv: map[string][]byte{},
	}
	fr.outputByArgv["doppler run -- docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" ps --format json"] = []byte(`[
		{"Service":"postgres","State":"running","Health":"unhealthy","Publishers":[{"URL":"0.0.0.0","TargetPort":5432,"PublishedPort":5432,"Protocol":"tcp"}]}
	]`)
	fr.outputByArgv[runtimeDockerPSArgv("shop")] = []byte(`[
			{"Labels":{"com.docktree.app":"shop","com.docktree.slug":"main","com.docktree.project":"shop-infra","com.docktree.service":"postgres"},"State":"running","Ports":"0.0.0.0:15432->5432/tcp"}
		]`)
	fr.outputByArgv[runtimeComposeLSArgv()] = []byte(`[
			{"Name":"shop-infra","Status":"running(1)"}
		]`)
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	err := runDoctor(context.Background(), nil, deps)
	if err == nil {
		t.Fatal("doctor should fail when error diagnostics are present")
	}
	got := stdout.String()
	for _, want := range []string{
		"daemon_unavailable",
		"secrets_preflight",
		"hardcoded_host_port",
		"shared_depends_on_non_shared",
		"shared_service_missing",
		"container_name",
		"named_volume",
		"external_volume",
		"infra_unhealthy",
		"infra_service_missing",
		"port_drift",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output =\n%s\nwant %q", got, want)
		}
	}
}

func TestDoctorSkipsInfraInspectionWhenNoSharedServicesAreConfigured(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, `
app = "shop"

[services.api]
host_port_env = "API_PORT"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		outputByArgv: map[string][]byte{
			"docker info --format {{json .ServerVersion}}": []byte(`"25.0.0"`),
			runtimeComposeLSArgv():                         []byte(`[]`),
			runtimeDockerPSArgv("shop"):                    []byte(`[]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runDoctor(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	for _, argv := range commandArgvStrings(fr.commands) {
		if strings.Contains(argv, "-p shop-infra") && strings.Contains(argv, " ps --format json") {
			t.Fatalf("doctor inspected infra despite no shared services: %#v", commandArgvStrings(fr.commands))
		}
	}
}

func TestPortDriftDiagnosticsIgnoresSiblingServicePorts(t *testing.T) {
	r := &resolved{
		names:        identity.Derive("shop", "feature_one"),
		servicePorts: map[string]int{"api": 3000},
		sharedPorts:  map[string]int{"postgres": 5432},
	}
	stacks := []dockerstate.Stack{
		{
			Project: "shop-feature_one",
			Services: []dockerstate.Service{{
				Project: "shop-feature_one",
				Name:    "api",
				Ports:   []dockerstate.Port{{Published: 3000}},
			}},
		},
		{
			Project: "shop-other",
			Services: []dockerstate.Service{{
				Project: "shop-other",
				Name:    "api",
				Ports:   []dockerstate.Port{{Published: 3999}},
			}},
		},
		{
			Project: "shop-infra",
			Services: []dockerstate.Service{{
				Project: "shop-infra",
				Name:    "postgres",
				Ports:   []dockerstate.Port{{Published: 5432}},
			}},
		},
	}
	if issues := portDriftDiagnostics(r, stacks); len(issues) != 0 {
		t.Fatalf("sibling stack port should not create current drift diagnostics: %#v", issues)
	}
}

func TestDoctorReportsConfiguredPortTokenMissingFromComposeAndRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, `
app = "shop"

[services.api]
host_port_env = "API_PORT"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"3000"}]}}}`),
		outputByArgv: map[string][]byte{
			"docker info --format {{json .ServerVersion}}": []byte(`"25.0.0"`),
			runtimeComposeLSArgv():                         []byte(`[{"Name":"shop-feature_one","Status":"running(1)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[
				{"Labels":{"com.docktree.app":"shop","com.docktree.slug":"feature_one","com.docktree.project":"shop-feature_one","com.docktree.service":"api"},"State":"running","Ports":""}
			]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	err := runDoctor(context.Background(), nil, deps)
	if err == nil {
		t.Fatal("doctor should fail when configured host port token is absent from compose")
	}
	got := stdout.String()
	for _, want := range []string{"missing_host_port_token", "port_missing"} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output =\n%s\nwant %q", got, want)
		}
	}
}

func writeProject(t *testing.T, root, config string) {
	t.Helper()
	writeBasicProject(t, root)
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}
