package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/ports"
	"github.com/rajpatil53/docktree/internal/registry"
	"github.com/rajpatil53/docktree/internal/runner"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func TestUpOrchestratesArtifactsInfraReadinessAndWorktreeCompose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stderr strings.Builder
	deps.stderr = &stderr

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	wantEvents := []string{
		"resolve",
		"write-env",
		"render-projections",
		"create-shared-network",
		"start-infra",
		"wait-shared",
		"compose-up",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}

	for _, path := range []string{
		paths.EnvFile(root),
		paths.InfraCompose(root),
		paths.WorktreeCompose(root),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}
	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envData), "POSTGRES_PORT=5432\n") {
		t.Fatalf("shared stable port was not written to env:\n%s", envData)
	}
	// The infra artifact must be worktree-invariant: stable infra project name
	// and registry-backed shared ports, but no per-worktree ports and no
	// worktree network — otherwise cross-worktree ups would change the infra
	// services' config hash and recreate the shared databases.
	infraEnvData, err := os.ReadFile(paths.InfraEnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(infraEnvData), "COMPOSE_PROJECT_NAME=shop-infra\n") ||
		!strings.Contains(string(infraEnvData), "POSTGRES_PORT=5432\n") {
		t.Fatalf("infra env artifact missing stable values:\n%s", infraEnvData)
	}
	if strings.Contains(string(infraEnvData), "API_PORT=") ||
		strings.Contains(string(infraEnvData), "DOCKTREE_WORKTREE_NETWORK=") {
		t.Fatalf("infra env artifact must not carry per-worktree values:\n%s", infraEnvData)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), ".docktree/.env.worktree") || !strings.Contains(string(worktreeData), "- .env.worktree") {
		t.Fatalf("projection env_file must be relative to .docktree compose file:\n%s", worktreeData)
	}
	infraData, err := os.ReadFile(paths.InfraCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(infraData), "- .env.worktree") || !strings.Contains(string(infraData), "- .env.infra") {
		t.Fatalf("infra projection must consume .env.infra, not the per-worktree artifact:\n%s", infraData)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker network create shop_shared") {
		t.Fatalf("network create command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" up -d") {
		t.Fatalf("infra up command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.EnvFile(root)+" -p shop-feature_one -f "+paths.WorktreeCompose(root)+" up -d --remove-orphans") {
		t.Fatalf("worktree up command missing from %#v", argv)
	}
	assertContainsInOrder(t, stderr.String(), []string{
		"docktree up: preparing stack artifacts\n",
		"docktree up: ensuring shared network shop_shared\n",
		"docktree up: starting shared infra shop-infra\n",
		"docktree up: waiting for shared services\n",
		"docktree up: starting worktree stack shop-feature_one\n",
	})
}

func TestUpAlwaysRunsIdempotentInfraUp(t *testing.T) {
	// Even when shared services are already running and healthy, up runs the
	// idempotent infra compose up -d: compose converges only on config change,
	// which is what delivers projection changes (e.g. new dt-upstream-* aliases)
	// to a running infra stack with no imperative reconcile.
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(basicComposeConfigJSON),
		outputByArgv: map[string][]byte{
			// Shared services report running+healthy; infra up must run anyway.
			"docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " ps --format json": []byte(`[
				{"Service":"postgres","State":"running","Health":"healthy"}
			]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stderr strings.Builder
	deps.stderr = &stderr

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" up -d") {
		t.Fatalf("infra up must run unconditionally: %#v", argv)
	}
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.EnvFile(root)+" -p shop-feature_one -f "+paths.WorktreeCompose(root)+" up -d --remove-orphans") {
		t.Fatalf("worktree up command missing from %#v", argv)
	}
	if !reflect.DeepEqual(events, []string{
		"resolve",
		"write-env",
		"render-projections",
		"create-shared-network",
		"start-infra",
		"wait-shared",
		"compose-up",
	}) {
		t.Fatalf("events = %#v, want unconditional start-infra", events)
	}
	assertContainsInOrder(t, stderr.String(), []string{
		"docktree up: starting shared infra shop-infra\n",
		"docktree up: waiting for shared services\n",
	})
}

func TestUpAdvertisesUserNamedDefaultNetwork(t *testing.T) {
	// A compose file may name its default network; DOCKTREE_WORKTREE_NETWORK
	// must advertise the network the services actually join, not <project>_default.
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(`{
	  "services": {
	    "api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
	    "postgres": {"ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]}
	  },
	  "networks": {
	    "default": {"name": "custom-net"}
	  }
	}`)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envData), "DOCKTREE_WORKTREE_NETWORK=custom-net\n") {
		t.Fatalf("env artifact must advertise the user-named default network:\n%s", envData)
	}
}

func TestUpPreservesPostgresLogicalForkFromExistingDatabase(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte("1\n"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), "DATABASE_URL=") {
		t.Fatalf("up should not write stateful DATABASE_URL:\n%s", envData)
	}
	if !strings.Contains(string(envData), "POSTGRES_PRIMARY_DB=shop_shared_feature_one\n") {
		t.Fatalf("up did not write detected isolated stateful env:\n%s", envData)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), "\n  postgres:") {
		t.Fatalf("logical Postgres fork should keep using shared infra service, got projection:\n%s", worktreeData)
	}
	if !strings.Contains(string(worktreeData), "com.docktree.data: isolated") {
		t.Fatalf("logical Postgres fork should label worktree services with isolated data mode:\n%s", worktreeData)
	}
}

func TestUpDiscoveredServicesDefaultToAutostart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte("app = \"shop\"\nshared = [\"postgres\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), "docktree-manual") {
		t.Fatalf("auto-discovered services should autostart by default:\n%s", worktreeData)
	}
}

func TestUpRejectsFatalComposeValidationErrorsBeforeWritingArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(`{
	  "services": {
	    "api": {},
	    "postgres": {"depends_on": {"api": {"condition": "service_started", "required": true}}}
	  }
	}`)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runUp(context.Background(), nil, deps)
	if err == nil || !strings.Contains(err.Error(), "shared service \"postgres\" depends on non-shared service \"api\"") {
		t.Fatalf("runUp error = %v, want fatal validation error", err)
	}
	if _, statErr := os.Stat(paths.EnvFile(root)); !os.IsNotExist(statErr) {
		t.Fatalf("env artifact was written despite fatal validation error: %v", statErr)
	}
}

func TestUpEmitsComposeValidationWarnings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(`{
	  "services": {
	    "api": {
	      "container_name": "fixed-api",
	      "ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]
	    },
	    "postgres": {"ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]}
	  }
	}`)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stderr strings.Builder
	deps.stderr = &stderr

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning container_name") {
		t.Fatalf("stderr = %q, want compose warning", stderr.String())
	}
}

func TestUpRunsInfraUpEvenWhenOnlyOneSharedServiceIsRunning(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:  []byte(statefulComposeConfigJSON),
		infraPSOutput: []byte("postgres\n"),
		outputByArgv:  map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" up -d") {
		t.Fatalf("partial infra should be reconciled with idempotent up -d --remove-orphans; commands=%#v", argv)
	}
}

func TestUpWaitsForSharedServicesWithoutHostPorts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "shop"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(`{
	  "services": {
	    "api": {
	      "depends_on": {"postgres": {"condition": "service_healthy", "required": true}},
	      "ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]
	    },
	    "postgres": {"expose": ["5432"]}
	  }
	}`)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{
		"resolve",
		"write-env",
		"render-projections",
		"create-shared-network",
		"start-infra",
		"wait-shared",
		"compose-up",
	}) {
		t.Fatalf("events = %#v, want wait-shared even without shared host ports", events)
	}
}

func TestWaitForSharedReadinessRequiresRunningHealthySharedServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{outputByArgv: map[string][]byte{}}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		t.Fatal(err)
	}
	psArgv := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " ps --format json"
	fr.outputByArgv[psArgv] = []byte(`[
		{"Service":"postgres","State":"running","Health":"healthy"}
	]`)

	if err := waitForSharedReadiness(context.Background(), r, deps, time.Millisecond, func(int) bool { return true }); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fr.outputByArgv[psArgv] = []byte(`[
		{"Service":"postgres","State":"running","Health":"starting"}
	]`)
	err = waitForSharedReadiness(ctx, r, deps, time.Millisecond, func(int) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("waitForSharedReadiness error = %v, want pending postgres", err)
	}
}

func TestUpMainWorktreeWritesComposeDefaultPort(t *testing.T) {
	root, _ := writeGitBackedBasicProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envData), "API_PORT=3000\n") {
		t.Fatalf("main worktree did not use compose default API_PORT:\n%s", envData)
	}
	if !strings.Contains(string(envData), "COMPOSE_PROJECT_NAME=shop\n") {
		t.Fatalf("main worktree did not use app project name:\n%s", envData)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.EnvFile(root)+" -p shop -f "+paths.WorktreeCompose(root)+" up -d --remove-orphans") {
		t.Fatalf("main worktree compose command should use app project name so containers are shop-<service>-1: %#v", argv)
	}
}

func TestUpMainWorktreeBindFailureDoesNotBumpDefaultPort(t *testing.T) {
	root, _ := writeGitBackedBasicProject(t)
	t.Setenv("HOME", t.TempDir())

	upArgv := "docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	fr := &fakeRunner{
		configOutput:     []byte(basicComposeConfigJSON),
		runErrOnceByArgv: map[string]error{upArgv: errors.New("Bind for 0.0.0.0:3000 failed: port is already allocated")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runUp(context.Background(), nil, deps)
	if err == nil || !strings.Contains(err.Error(), "main worktree") || !strings.Contains(err.Error(), "3000") {
		t.Fatalf("runUp error = %v, want main worktree default-port bind failure", err)
	}

	envData, readErr := os.ReadFile(paths.EnvFile(root))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(envData), "API_PORT=3000\n") {
		t.Fatalf("main worktree bind failure should leave API_PORT at 3000:\n%s", envData)
	}
	if got := strings.Count(strings.Join(commandArgvStrings(fr.commands), "\n"), upArgv); got != 1 {
		t.Fatalf("main worktree compose up count = %d, want no retry; commands=%#v", got, commandArgvStrings(fr.commands))
	}
}

func TestUpRetriesWorktreeComposeBindFailureWithNextPort(t *testing.T) {
	_, root := writeGitBackedBasicProject(t)
	t.Setenv("HOME", t.TempDir())

	initial, err := ports.Resolve(registry.BandStart, "feature_one", registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	next, err := ports.ResolveAfterBindFailure(registry.BandStart, initial, registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	upArgv := "docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	fr := &fakeRunner{
		configOutput:     []byte(basicComposeConfigJSON),
		runErrOnceByArgv: map[string]error{upArgv: errors.New("Bind for 0.0.0.0:" + strconv.Itoa(initial) + " failed: port is already allocated")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stderr strings.Builder
	deps.stderr = &stderr

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envData), "API_PORT="+strconv.Itoa(next)+"\n") {
		t.Fatalf("bind retry did not rewrite API_PORT to %d after failed %d:\n%s", next, initial, envData)
	}
	if got := strings.Count(strings.Join(commandArgvStrings(fr.commands), "\n"), upArgv); got != 2 {
		t.Fatalf("worktree compose up count = %d, want retry once; commands=%#v", got, commandArgvStrings(fr.commands))
	}
	assertContainsInOrder(t, stderr.String(), []string{
		"docktree up: starting worktree stack shop-feature_one\n",
		"docktree up: retrying worktree stack shop-feature_one after bind failure\n",
	})
}

func TestUpBindFailureRetryLeavesFixedUnrelatedPortsUnchanged(t *testing.T) {
	_, root := writeGitBackedBasicProject(t)
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "shop"

[services.api]
host_port_env = "API_PORT"

[services.webhook]
host_port_env = "WEBHOOK_PORT"
fixed = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	model := `{
	  "services": {
	    "api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
	    "webhook": {"ports": [{"target": 9000, "published": "${WEBHOOK_PORT:-9000}"}]}
	  }
	}`
	apiInitial, err := ports.Resolve(registry.BandStart, "feature_one", registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	apiNext, err := ports.ResolveAfterBindFailure(registry.BandStart, apiInitial, registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	webhookInitial, err := ports.Resolve(registry.BandStart+registry.BandWidth, "feature_one", registry.BandWidth, true, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	upArgv := "docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	fr := &fakeRunner{
		configOutput:     []byte(model),
		runErrOnceByArgv: map[string]error{upArgv: errors.New("Bind for 0.0.0.0:" + strconv.Itoa(apiInitial) + " failed: port is already allocated")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envData), "API_PORT="+strconv.Itoa(apiNext)+"\n") {
		t.Fatalf("API_PORT was not bumped to %d:\n%s", apiNext, envData)
	}
	if !strings.Contains(string(envData), "WEBHOOK_PORT="+strconv.Itoa(webhookInitial)+"\n") {
		t.Fatalf("fixed unrelated WEBHOOK_PORT changed from %d:\n%s", webhookInitial, envData)
	}
}

func TestUpProvisionsDeclarativeGenericIsolatedStatefulService(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
default_strategy = "isolated"
engine = "redis"
snapshot_source = "shop_redisdata"
env = { REDIS_NAMESPACE = "redis_{slug}" }
`)
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
		runErrByArgv: map[string]error{volumeInspectArgv(stateful.VolumeName("shop", "feature_one", "redis")): errors.New("no such volume")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker volume create --label com.docktree.managed=true --label com.docktree.app=shop --label com.docktree.slug=feature_one --label com.docktree.project=shop-feature_one --label com.docktree.service=redis --label com.docktree.fork=redis shop-feature_one-redisdata") {
		t.Fatalf("declarative isolated redis did not create fork volume: %#v", argv)
	}
	if !containsArgv(argv, "docker run --rm --mount type=volume,src=shop_redisdata,dst=/from,readonly --mount type=volume,src=shop-feature_one-redisdata,dst=/to alpine sh -c cp -a /from/. /to/") {
		t.Fatalf("declarative isolated redis did not snapshot source volume: %#v", argv)
	}
	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), "REDIS_URL=") {
		t.Fatalf("up should not write stateful REDIS_URL:\n%s", envData)
	}
	if !strings.Contains(string(envData), "REDIS_NAMESPACE=redis_feature_one\n") {
		t.Fatalf("up did not write isolated redis env:\n%s", envData)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worktreeData), "shop-feature_one-redisdata") {
		t.Fatalf("isolated redis projection missing forked volume:\n%s", worktreeData)
	}
}

func TestUpProvisionsDeclarativePostgresIsolatedStatefulService(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	data, err := os.ReadFile(filepath.Join(root, "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "engine = \"postgres\"", "default_strategy = \"isolated\"\nengine = \"postgres\"", 1))
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte("1\n"),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c CREATE DATABASE \"shop_shared_feature_one\" WITH TEMPLATE \"shop_shared\";") {
		t.Fatalf("declarative isolated postgres did not clone logical database: %#v", argv)
	}
	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), "DATABASE_URL=") {
		t.Fatalf("up should not write stateful DATABASE_URL:\n%s", envData)
	}
	if !strings.Contains(string(envData), "POSTGRES_PRIMARY_DB=shop_shared_feature_one\n") {
		t.Fatalf("up did not write declarative isolated stateful env:\n%s", envData)
	}
}

func writeGitBackedBasicProject(t *testing.T) (string, string) {
	return writeGitBackedProjectWithConfig(t, "")
}

func writeGitBackedProjectWithConfig(t *testing.T, config string) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	main := filepath.Join(tmp, "Shop-Main")
	writeBasicProject(t, main)
	if config != "" {
		writeProject(t, main, config)
	}
	runGit(t, main, "init")
	runGit(t, main, "config", "user.email", "docktree@example.test")
	runGit(t, main, "config", "user.name", "Docktree Test")
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-m", "initial")
	feature := filepath.Join(tmp, "Feature-One")
	runGit(t, main, "worktree", "add", "-b", "feature-one", feature)
	return main, feature
}

func containsArgv(argv []string, want string) bool {
	for _, got := range argv {
		if got == want {
			return true
		}
	}
	return false
}

const envOverrideProjectConfig = `
app = "shop"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"

[services.postgres]
host_port_env = "POSTGRES_PORT"

[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"
`

func envOverridePairs(t *testing.T, path string) []string {
	t.Helper()
	values, err := envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) == 0 {
		t.Fatalf("env artifact %s is empty", path)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values[key])
	}
	return pairs
}

func TestUpReappliesArtifactEnvAfterSecretsWrapperInjection(t *testing.T) {
	_, root := writeGitBackedProjectWithConfig(t, envOverrideProjectConfig+`
[secrets]
wrapper = "doppler run --"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	worktreePairs := envOverridePairs(t, paths.EnvFile(root))
	if !containsArgv(worktreePairs, "OPENSEARCH_INDEX_PREFIX=feature_one_") {
		t.Fatalf("worktree artifact missing branch-slug [env] value: %#v", worktreePairs)
	}
	infraPairs := envOverridePairs(t, paths.InfraEnvFile(root))
	if !containsArgv(infraPairs, "OPENSEARCH_INDEX_PREFIX=shop_main_") {
		t.Fatalf("infra artifact must carry main-slug [env] values: %#v", infraPairs)
	}
	argv := commandArgvStrings(fr.commands)
	wantWorktree := "doppler run -- env -- " + strings.Join(worktreePairs, " ") +
		" docker compose --progress plain --env-file " + paths.EnvFile(root) +
		" -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	if !containsArgv(argv, wantWorktree) {
		t.Fatalf("worktree up must interpose artifact env after the wrapper:\nwant %q\ngot %#v", wantWorktree, argv)
	}
	wantInfra := "doppler run -- env -- " + strings.Join(infraPairs, " ") +
		" docker compose --progress plain --env-file " + paths.InfraEnvFile(root) +
		" -p shop-infra -f " + paths.InfraCompose(root) + " up -d"
	if !containsArgv(argv, wantInfra) {
		t.Fatalf("infra up must interpose the worktree-invariant artifact env:\nwant %q\ngot %#v", wantInfra, argv)
	}
	// Discovery parses ${VAR} placeholders with --no-interpolate and must stay
	// override-free; a leaked interposer here would also poison the model the
	// env artifact itself is derived from.
	wantDiscovery := "doppler run -- docker compose -f " + filepath.Join(root, "compose.yaml") +
		" config --no-interpolate --no-normalize --format json"
	if !containsArgv(argv, wantDiscovery) {
		t.Fatalf("discovery config command must stay override-free:\nwant %q\ngot %#v", wantDiscovery, argv)
	}
}

func TestUpBindRetryRebuildsEnvOverridesFromRewrittenArtifact(t *testing.T) {
	_, root := writeGitBackedProjectWithConfig(t, envOverrideProjectConfig+`
[secrets]
wrapper = "doppler run --"
`)
	t.Setenv("HOME", t.TempDir())

	initial, err := ports.Resolve(registry.BandStart, "feature_one", registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	next, err := ports.ResolveAfterBindFailure(registry.BandStart, initial, registry.BandWidth, false, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	worktreeUpSubstr := " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	fr := &fakeRunner{
		configOutput: []byte(basicComposeConfigJSON),
		runErrOnceBySubstr: map[string]error{
			worktreeUpSubstr: errors.New("Bind for 0.0.0.0:" + strconv.Itoa(initial) + " failed: port is already allocated"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	var attempts []string
	for _, got := range commandArgvStrings(fr.commands) {
		if strings.Contains(got, worktreeUpSubstr) {
			attempts = append(attempts, got)
		}
	}
	if len(attempts) != 2 {
		t.Fatalf("worktree up attempts = %d, want 2: %#v", len(attempts), attempts)
	}
	if !strings.HasPrefix(attempts[0], "doppler run -- env -- ") ||
		!strings.Contains(attempts[0], " API_PORT="+strconv.Itoa(initial)+" ") {
		t.Fatalf("first attempt must interpose the original port %d: %q", initial, attempts[0])
	}
	if !strings.Contains(attempts[1], " API_PORT="+strconv.Itoa(next)+" ") {
		t.Fatalf("retry must interpose the rewritten artifact's port %d: %q", next, attempts[1])
	}
}

func TestUpAppendsArtifactEnvToProcessEnvWithoutWrapper(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, envOverrideProjectConfig)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	pairs := envOverridePairs(t, paths.EnvFile(root))
	want := "docker compose --progress plain --env-file " + paths.EnvFile(root) +
		" -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	var command *runner.Command
	for i := range fr.commands {
		if strings.Join(fr.commands[i].Argv, " ") == want {
			command = &fr.commands[i]
			break
		}
	}
	if command == nil {
		t.Fatalf("worktree up argv must stay unwrapped without a wrapper, missing %q in %#v", want, commandArgvStrings(fr.commands))
	}
	if len(command.Env) < len(pairs) {
		t.Fatalf("worktree up env too short: %#v", command.Env)
	}
	gotSuffix := command.Env[len(command.Env)-len(pairs):]
	if !reflect.DeepEqual(gotSuffix, pairs) {
		t.Fatalf("worktree up env must end with artifact overrides:\nwant %#v\ngot %#v", pairs, gotSuffix)
	}
}
