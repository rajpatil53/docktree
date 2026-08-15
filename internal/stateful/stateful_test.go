package stateful

import (
	"reflect"
	"strings"
	"testing"
)

func TestSafetyGuardsPermitOnlyOwnIsolatedResources(t *testing.T) {
	req := DestroyRequest{
		App:            "shop",
		Slug:           "feature_one",
		MainSlug:       "main",
		Service:        "postgres",
		Strategy:       "isolated",
		ResourceKind:   ResourceVolume,
		ResourceName:   VolumeName("shop", "feature_one", "postgres"),
		SnapshotSource: "shop_pgdata",
	}
	if err := CheckDestroy(req); err != nil {
		t.Fatalf("own isolated volume should be destroyable: %v", err)
	}

	req.ResourceName = "shop_pgdata"
	if err := CheckDestroy(req); err == nil {
		t.Fatal("shared snapshot source must not be destroyable")
	}

	req.ResourceName = VolumeName("shop", "main", "postgres")
	if err := CheckDestroy(req); err == nil {
		t.Fatal("main store must not be destroyable")
	}

	req.ResourceName = VolumeName("shop", "feature_one", "postgres")
	req.Strategy = ""
	if err := CheckDestroy(req); err == nil {
		t.Fatal("unknown strategy must be unsafe")
	}

	req.Strategy = "shared"
	if err := CheckDestroy(req); err == nil {
		t.Fatal("shared strategy must be unsafe for destructive operations")
	}
}

func TestSafetyGuardsProtectLogicalDBs(t *testing.T) {
	req := DestroyRequest{
		App:          "shop",
		Slug:         "feature_one",
		MainSlug:     "main",
		Service:      "postgres",
		Strategy:     "isolated",
		ResourceKind: ResourceLogicalDB,
		ResourceName: TargetDB("shop_shared", "feature_one"),
		SourceDB:     "shop_shared",
	}
	if err := CheckDestroy(req); err != nil {
		t.Fatalf("own isolated logical DB should be destroyable: %v", err)
	}

	req.ResourceName = "shop_shared"
	if err := CheckDestroy(req); err == nil {
		t.Fatal("source logical DB must not be destroyable")
	}

	req.Slug = "feature_one"
	req.ResourceName = TargetDB("shop_shared", "other_feature")
	if err := CheckDestroy(req); err == nil {
		t.Fatal("other worktree logical DB must not be destroyable")
	}
}

func TestGenericVolumeSnapshotHelpers(t *testing.T) {
	vol := VolumeName("shop", "feature_one", "postgres")
	if vol != "shop-feature_one-postgresdata" {
		t.Fatalf("volume name = %q", vol)
	}
	if src := SnapshotSource("shop", "postgres", ""); src != "shop_postgresdata" {
		t.Fatalf("default source = %q", src)
	}
	if src := SnapshotSource("shop", "postgres", "baseline_pgdata"); src != "baseline_pgdata" {
		t.Fatalf("configured source = %q", src)
	}
	for _, name := range []string{vol, "baseline_pgdata", "redis.data-1"} {
		if err := ValidateVolumeName(name); err != nil {
			t.Fatalf("ValidateVolumeName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "/", "../secret", "host:path", "bad/name"} {
		if err := ValidateVolumeName(name); err == nil {
			t.Fatalf("ValidateVolumeName(%q) returned nil, want rejection", name)
		}
	}
	labels := VolumeLabels("shop", "feature_one", "shop-feature_one", "postgres")
	if labels[LabelFork] != "postgres" || labels[LabelApp] != "shop" || labels[LabelSlug] != "feature_one" {
		t.Fatalf("labels = %#v", labels)
	}
	if !IsForkVolume(labels, "postgres") {
		t.Fatalf("fork label was not detected: %#v", labels)
	}
	if !strings.Contains(QuiesceWarning("shop_pgdata"), "quiesced") {
		t.Fatalf("quiesce warning missing: %q", QuiesceWarning("shop_pgdata"))
	}

	create := VolumeCreateArgv("shop", "feature_one", "shop-feature_one", "postgres")
	for _, want := range []string{
		"docker", "volume", "create",
		"--label", LabelApp + "=shop",
		"--label", LabelFork + "=postgres",
		vol,
	} {
		if !contains(create, want) {
			t.Fatalf("create argv = %#v, missing %q", create, want)
		}
	}

	cp := VolumeSnapshotArgv("shop_pgdata", vol)
	want := []string{"docker", "run", "--rm", "--mount", "type=volume,src=shop_pgdata,dst=/from,readonly", "--mount", "type=volume,src=" + vol + ",dst=/to", "alpine", "sh", "-c", "cp -a /from/. /to/"}
	if !reflect.DeepEqual(cp, want) {
		t.Fatalf("snapshot argv = %#v, want %#v", cp, want)
	}
}

func TestParseForkVolumesFromDockerLabels(t *testing.T) {
	volumes, err := ParseVolumeListJSON([]byte(`[
		{"Name":"shop-feature_one-postgresdata","Labels":"com.docktree.app=shop,com.docktree.slug=feature_one,com.docktree.project=shop-feature_one,com.docktree.service=postgres,com.docktree.fork=postgres"},
		{"Name":"other","Labels":{"com.docktree.app":"shop"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 2 {
		t.Fatalf("volumes = %#v", volumes)
	}
	if !volumes[0].IsForkFor("postgres") || volumes[0].Slug != "feature_one" || volumes[0].Project != "shop-feature_one" {
		t.Fatalf("fork volume not parsed: %#v", volumes[0])
	}
	if volumes[1].IsForkFor("postgres") {
		t.Fatalf("non-fork volume detected as fork: %#v", volumes[1])
	}
}

func TestParseForkVolumesFromLineDelimitedDockerJSON(t *testing.T) {
	volumes, err := ParseVolumeListJSON([]byte(`{"Name":"shop-feature_one-redisdata","Labels":{"docktree.app":"shop","docktree.slug":"feature_one","docktree.project":"shop-feature_one","docktree.service":"redis","docktree.fork":"redis"}}
{"Name":"scratch","Labels":""}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 2 {
		t.Fatalf("volumes = %#v", volumes)
	}
	if volumes[0].App != "shop" || volumes[0].Slug != "feature_one" || !volumes[0].IsForkFor("redis") {
		t.Fatalf("line-delimited fork volume = %#v", volumes[0])
	}
	if volumes[1].IsForkFor("") {
		t.Fatalf("unlabeled volume detected as fork: %#v", volumes[1])
	}
}

func TestPostgresFastPathAndDBHelpers(t *testing.T) {
	src := SourceDB("shop_shared")
	dst := TargetDB(src, "feature_one")
	if src != "shop_shared" || dst != "shop_shared_feature_one" {
		t.Fatalf("db names = %q/%q", src, dst)
	}
	if SourceDB("") != "" {
		t.Fatalf("empty source DB should stay empty")
	}
	if !UsePostgresFastPath("postgresql") {
		t.Fatal("postgresql should use the Postgres fast path")
	}
	if UsePostgresFastPath("") {
		t.Fatal("empty engine should use the generic volume snapshot path")
	}
	if UsePostgresFastPath("mysql") {
		t.Fatal("unsupported engines must fall back to generic volume snapshots")
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
