package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
)

func TestForkPostgresRegeneratesArtifactsWithIsolatedDataSource(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte("1\n"),
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runFork(context.Background(), []string{"postgres"}, deps); err != nil {
		t.Fatal(err)
	}

	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), "DATABASE_URL=") {
		t.Fatalf("fork should not write stateful DATABASE_URL:\n%s", envData)
	}
	if !strings.Contains(string(envData), "POSTGRES_PRIMARY_DB=shop_shared_feature_one\n") {
		t.Fatalf("fork did not write isolated stateful env:\n%s", envData)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), "\n  postgres:") || strings.Contains(string(worktreeData), "shop-feature_one-postgresdata") {
		t.Fatalf("Postgres logical fork should not render a local volume-backed service:\n%s", worktreeData)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c CREATE DATABASE \"shop_shared_feature_one\" WITH TEMPLATE \"shop_shared\";") {
		t.Fatalf("postgres TEMPLATE clone command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.EnvFile(root)+" -p shop-feature_one -f "+paths.WorktreeCompose(root)+" up -d --remove-orphans --force-recreate") {
		t.Fatalf("fork did not recreate worktree stack with isolated artifacts: %#v", argv)
	}
}

func TestForkPostgresHonorsConfiguredSuperuser(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	data, err := os.ReadFile(filepath.Join(root, "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `source_db = "shop_shared"`, "source_db = \"shop_shared\"\nsuperuser = \"primary\"", 1))
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	existsArgv := func(db string) string {
		return "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U primary -d postgres -tAc SELECT 1 FROM pg_database WHERE datname = '" + db + "';"
	}
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			existsArgv("shop_shared"):             []byte("1\n"),
			forkVolumeListArgv("shop"):            []byte(""),
			existsArgv("shop_shared_feature_one"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runFork(context.Background(), []string{"postgres"}, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	want := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U primary -d postgres -v ON_ERROR_STOP=1 -c CREATE DATABASE \"shop_shared_feature_one\" WITH TEMPLATE \"shop_shared\";"
	if !containsArgv(argv, want) {
		t.Fatalf("configured superuser not used in clone command: %#v", argv)
	}
	for _, cmd := range argv {
		if strings.Contains(cmd, "-U postgres") {
			t.Fatalf("command used default superuser despite configured primary: %q", cmd)
		}
	}
}

func TestForkPostgresFallbackDropsPartialCloneAndStreamsDump(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	fast := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c CREATE DATABASE \"shop_shared_feature_one\" WITH TEMPLATE \"shop_shared\";"
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte("1\n"),
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
		runErrByArgv: map[string]error{fast: errors.New("source database is being accessed by other users")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runFork(context.Background(), []string{"postgres"}, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c DROP DATABASE IF EXISTS \"shop_shared_feature_one\" WITH (FORCE);") {
		t.Fatalf("fallback did not drop partial clone: %#v", argv)
	}
	if !containsArgv(argv, "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" exec -T postgres sh -c set -e; dump=$(mktemp); trap 'rm -f \"$dump\"' EXIT; createdb -U postgres -- 'shop_shared_feature_one'; pg_dump -U postgres --file=\"$dump\" -- 'shop_shared'; psql -U postgres -d 'shop_shared_feature_one' --file=\"$dump\"") {
		t.Fatalf("fallback dump/restore command missing from %#v", argv)
	}
}

func TestForkPostgresRefusesMissingSourceDatabase(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte(""),
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runFork(context.Background(), []string{"postgres"}, deps)
	if err == nil || !strings.Contains(err.Error(), "source database") {
		t.Fatalf("runFork error = %v, want source database guard", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "CREATE DATABASE") {
		t.Fatalf("fork attempted clone without source database: %#v", commandArgvStrings(fr.commands))
	}
}

func TestForkPostgresFallbackDropsDestinationWhenDumpFails(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	fast := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c CREATE DATABASE \"shop_shared_feature_one\" WITH TEMPLATE \"shop_shared\";"
	fallback := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres sh -c set -e; dump=$(mktemp); trap 'rm -f \"$dump\"' EXIT; createdb -U postgres -- 'shop_shared_feature_one'; pg_dump -U postgres --file=\"$dump\" -- 'shop_shared'; psql -U postgres -d 'shop_shared_feature_one' --file=\"$dump\""
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte("1\n"),
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
		runErrByArgv: map[string]error{
			fast:     errors.New("source database is being accessed by other users"),
			fallback: errors.New("pg_dump failed"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runFork(context.Background(), []string{"postgres"}, deps)
	if err == nil || !strings.Contains(err.Error(), "pg_dump failed") {
		t.Fatalf("runFork error = %v, want fallback failure", err)
	}
	drop := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c DROP DATABASE IF EXISTS \"shop_shared_feature_one\" WITH (FORCE);"
	if got := strings.Count(strings.Join(commandArgvStrings(fr.commands), "\n"), drop); got != 2 {
		t.Fatalf("drop count = %d, want cleanup before and after failed fallback; commands=%#v", got, commandArgvStrings(fr.commands))
	}
}

func TestForkPostgresRequiresSourceDB(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	data, err := os.ReadFile(filepath.Join(root, "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `source_db = "shop_shared"`, ``, 1))
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):                      []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err = runFork(context.Background(), []string{"postgres"}, deps)
	if err == nil || !strings.Contains(err.Error(), "source_db") {
		t.Fatalf("runFork error = %v, want source_db guard", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "CREATE DATABASE") {
		t.Fatalf("fork attempted SQL despite missing source_db: %#v", commandArgvStrings(fr.commands))
	}
}

func TestForkPostgresRefusesExistingDestinationDatabase(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			postgresDatabaseExistsArgv(root, "shop_shared"):             []byte("1\n"),
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte("1\n"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runFork(context.Background(), []string{"postgres"}, deps)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("runFork error = %v, want existing destination guard", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "CREATE DATABASE") {
		t.Fatalf("fork attempted to recreate an existing logical database: %#v", commandArgvStrings(fr.commands))
	}
}

func TestForkUnsupportedEngineFallsBackToGenericSnapshot(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
		runErrByArgv: map[string]error{volumeInspectArgv("shop-feature_one-redisdata"): errors.New("no such volume")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stderr strings.Builder
	deps.stderr = &stderr

	if err := runFork(context.Background(), []string{"redis"}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "must be quiesced") {
		t.Fatalf("generic snapshot should report quiesce requirement, got %q", stderr.String())
	}
	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker volume create --label com.docktree.managed=true --label com.docktree.app=shop --label com.docktree.slug=feature_one --label com.docktree.project=shop-feature_one --label com.docktree.service=redis --label com.docktree.fork=redis shop-feature_one-redisdata") {
		t.Fatalf("volume create command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker run --rm --mount type=volume,src=shop_redisdata,dst=/from,readonly --mount type=volume,src=shop-feature_one-redisdata,dst=/to alpine sh -c cp -a /from/. /to/") {
		t.Fatalf("volume snapshot command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker compose --progress plain --env-file "+paths.EnvFile(root)+" -p shop-feature_one -f "+paths.WorktreeCompose(root)+" up -d --remove-orphans --force-recreate") {
		t.Fatalf("generic fork did not recreate worktree stack: %#v", argv)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), "${REDIS_PORT") {
		t.Fatalf("forked shared service should not publish the infra host port:\n%s", worktreeData)
	}
	if !strings.Contains(string(worktreeData), "shop-feature_one-redisdata") {
		t.Fatalf("forked redis volume missing from projection:\n%s", worktreeData)
	}
}

func TestForkGenericVolumeKeepsInspectProbesQuiet(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
		runErrByArgv: map[string]error{volumeInspectArgv("shop-feature_one-redisdata"): errors.New("no such volume")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout, stderr strings.Builder
	deps.stdout = &stdout
	deps.stderr = &stderr

	if err := runFork(context.Background(), []string{"redis"}, deps); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range fr.commands {
		if strings.Join(cmd.Argv, " ") != volumeInspectArgv("shop_redisdata") &&
			strings.Join(cmd.Argv, " ") != volumeInspectArgv("shop-feature_one-redisdata") {
			continue
		}
		if cmd.Stdout != io.Discard || cmd.Stderr != io.Discard {
			t.Fatalf("volume inspect probe should be quiet, got stdout=%T stderr=%T for %#v", cmd.Stdout, cmd.Stderr, cmd.Argv)
		}
	}
}

func TestForkGenericVolumeRemovesDestinationWhenSnapshotFails(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)
	snapshot := "docker run --rm --mount type=volume,src=shop_redisdata,dst=/from,readonly --mount type=volume,src=shop-feature_one-redisdata,dst=/to alpine sh -c cp -a /from/. /to/"
	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
		runErrByArgv: map[string]error{
			volumeInspectArgv("shop-feature_one-redisdata"): errors.New("no such volume"),
			snapshot: errors.New("copy failed"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runFork(context.Background(), []string{"redis"}, deps)
	if err == nil || !strings.Contains(err.Error(), "copy failed") {
		t.Fatalf("runFork error = %v, want snapshot failure", err)
	}
	if !containsArgv(commandArgvStrings(fr.commands), "docker volume rm shop-feature_one-redisdata") {
		t.Fatalf("snapshot failure did not remove destination volume: %#v", commandArgvStrings(fr.commands))
	}
}

func TestForkGenericVolumeRefusesMissingSourceOrExistingDestination(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)

	for _, tc := range []struct {
		name   string
		errs   map[string]error
		want   string
		forbid string
	}{
		{
			name:   "missing source",
			errs:   map[string]error{volumeInspectArgv("shop_redisdata"): errors.New("no such volume")},
			want:   "source volume",
			forbid: "docker run --rm --mount type=volume,src=shop_redisdata",
		},
		{
			name:   "existing destination",
			errs:   map[string]error{},
			want:   "already exists",
			forbid: "docker run --rm --mount type=volume,src=shop_redisdata",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runErrs := map[string]error{}
			for key, err := range tc.errs {
				runErrs[key] = err
			}
			if tc.name != "existing destination" {
				runErrs[volumeInspectArgv("shop-feature_one-redisdata")] = errors.New("no such volume")
			}
			fr := &fakeRunner{
				configOutput: []byte(statefulComposeConfigJSON),
				outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
				runErrByArgv: runErrs,
			}
			var events []string
			deps := newCommandTestDeps(root, fr, &events)

			err := runFork(context.Background(), []string{"redis"}, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runFork error = %v, want %q", err, tc.want)
			}
			if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), tc.forbid) {
				t.Fatalf("fork attempted snapshot despite guard: %#v", commandArgvStrings(fr.commands))
			}
		})
	}
}

func TestForkGenericVolumeRejectsUnsafeSnapshotSource(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "/"
`)

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runFork(context.Background(), []string{"redis"}, deps)
	if err == nil || !strings.Contains(err.Error(), "invalid Docker volume name") {
		t.Fatalf("runFork error = %v, want unsafe snapshot source rejection", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "docker run --rm") {
		t.Fatalf("fork attempted snapshot with unsafe source: %#v", commandArgvStrings(fr.commands))
	}
}

func TestForkGenericVolumeTargetsConfiguredSnapshotSource(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)
	fr := &fakeRunner{configOutput: []byte(`{
		  "services": {
		    "api": {
		      "depends_on": {"redis": {"condition": "service_started", "required": true}},
		      "ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]
		    },
		    "postgres": {"ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]},
		    "redis": {
		      "volumes": [
		        {"type":"volume","source":"scratchdata","target":"/scratch"},
	        {"type":"volume","source":"shop_redisdata","target":"/data"}
	      ],
	      "ports": [{"target": 6379, "published": "${REDIS_PORT:-6379}"}]
	    }
	  },
	  "volumes": {"scratchdata": {}, "shop_redisdata": {}}
	}`), outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte("")}, runErrByArgv: map[string]error{volumeInspectArgv("shop-feature_one-redisdata"): errors.New("no such volume")}}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runFork(context.Background(), []string{"redis"}, deps); err != nil {
		t.Fatal(err)
	}

	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worktreeData), "source: scratchdata") || !strings.Contains(string(worktreeData), "target: /scratch") {
		t.Fatalf("non-state volume should be left untouched:\n%s", worktreeData)
	}
	if !strings.Contains(string(worktreeData), "source: shop-feature_one-redisdata") || !strings.Contains(string(worktreeData), "target: /data") {
		t.Fatalf("configured snapshot source was not replaced at /data:\n%s", worktreeData)
	}
}

const statefulComposeConfigJSON = `{
  "services": {
    "api": {
      "depends_on": {"postgres": {"condition": "service_healthy", "required": true}},
      "env_file": [".env"],
      "ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]
    },
    "postgres": {
      "volumes": [{"type":"volume","source":"shop_pgdata","target":"/var/lib/postgresql/data"}],
      "ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]
    },
    "redis": {
      "volumes": [{"type":"volume","source":"shop_redisdata","target":"/data"}],
      "ports": [{"target": 6379, "published": "${REDIS_PORT:-6379}"}]
    }
  },
  "volumes": {
    "shop_pgdata": {},
    "shop_redisdata": {}
  }
}`

func writeGitBackedProject(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	main := filepath.Join(tmp, "Shop-Main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init")
	runGit(t, main, "config", "user.email", "docktree@example.test")
	runGit(t, main, "config", "user.name", "Docktree Test")
	writeStatefulProjectFiles(t, main)
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-m", "initial")
	feature := filepath.Join(tmp, "Feature-One")
	runGit(t, main, "worktree", "add", "-b", "feature-one", feature)
	return feature
}

func writeStatefulProjectFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "shop"
shared = ["postgres", "redis"]

[services.api]
host_port_env = "API_PORT"

[services.postgres]
host_port_env = "POSTGRES_PORT"

[services.redis]
host_port_env = "REDIS_PORT"

[stateful.postgres]
engine = "postgres"
snapshot_source = "shop_pgdata"
source_db = "shop_shared"
env = { POSTGRES_PRIMARY_DB = "shop_shared_{slug}" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendConfig(t *testing.T, root, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, "docktree.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func postgresDatabaseExistsArgv(root, db string) string {
	return "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U postgres -d postgres -tAc SELECT 1 FROM pg_database WHERE datname = '" + db + "';"
}

func volumeInspectArgv(name string) string {
	return "docker volume inspect " + name
}
