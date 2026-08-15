package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLSJSONUsesStableRuntimeShape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), outputByArgv: runtimeOutputs()}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runLS(context.Background(), []string{"--json"}, deps); err != nil {
		t.Fatal(err)
	}
	var stacks []struct {
		App      string `json:"app"`
		Slug     string `json:"slug"`
		Branch   string `json:"branch"`
		Project  string `json:"project"`
		Status   string `json:"status"`
		Services []struct {
			Name string `json:"service"`
		} `json:"services"`
		URLs []struct {
			Service string `json:"service"`
			URL     string `json:"url"`
		} `json:"urls"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &stacks); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
	}
	if len(stacks) != 1 {
		t.Fatalf("stacks = %#v, want one", stacks)
	}
	got := stacks[0]
	if got.App != "shop" || got.Slug != "feature_one" || got.Project != "shop-feature_one" || got.Status != "running(1)" {
		t.Fatalf("stack = %#v", got)
	}
	if len(got.URLs) != 1 || got.URLs[0].Service != "api" || got.URLs[0].URL != "http://localhost:18080" {
		t.Fatalf("urls = %#v", got.URLs)
	}
}

func TestLSJSONIncludesGitBranchForLiveWorktrees(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), outputByArgv: runtimeOutputs()}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runLS(context.Background(), []string{"--json"}, deps); err != nil {
		t.Fatal(err)
	}
	var stacks []struct {
		Slug   string `json:"slug"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &stacks); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
	}
	if len(stacks) != 1 || stacks[0].Slug != "feature_one" || stacks[0].Branch != "feature-one" {
		t.Fatalf("stacks = %#v, want feature branch", stacks)
	}
}

func TestLSFiltersOutUnrelatedComposeProjects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	outputs := runtimeOutputs()
	outputs[runtimeComposeLSArgv()] = []byte(`[
			{"Name":"other-app","Status":"running(1)"},
			{"Name":"shop-feature_one","Status":"running(1)"}
		]`)
	fr := &fakeRunner{outputByArgv: outputs}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runLS(context.Background(), []string{"--json"}, deps); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "other-app") {
		t.Fatalf("ls included unrelated compose project:\n%s", stdout.String())
	}
}

func TestOpenSelectsURLByWorktreeAndService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), outputByArgv: runtimeOutputs()}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runOpen(context.Background(), []string{"feature-one", "api"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "http://localhost:18080" {
		t.Fatalf("open output = %q, want API URL", got)
	}
}

func TestOpenFallsBackForComposeDiscoveredService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte("app = \"shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(basicComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv():      []byte(`[]`),
			runtimeDockerPSArgv("shop"): []byte(`[]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runOpen(context.Background(), []string{"api"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.HasPrefix(got, "http://localhost:") {
		t.Fatalf("open fallback output = %q, want discovered api URL", got)
	}
}

func TestOpenFallsBackToResolvedPortWhenRuntimeURLIsUnavailable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{outputByArgv: map[string][]byte{
		runtimeComposeLSArgv():      []byte(`[]`),
		runtimeDockerPSArgv("shop"): []byte(`[]`),
	}}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runOpen(context.Background(), []string{"api"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.HasPrefix(got, "http://localhost:") {
		t.Fatalf("open fallback output = %q, want localhost URL", got)
	}
}

func runtimeOutputs() map[string][]byte {
	return map[string][]byte{
		runtimeComposeLSArgv(): []byte(`[
				{"Name":"shop-feature_one","Status":"running(1)"}
			]`),
		runtimeDockerPSArgv("shop"): []byte(`[
				{"Labels":{"com.docktree.app":"shop","com.docktree.slug":"feature_one","com.docktree.project":"shop-feature_one","com.docktree.service":"api"},"State":"running","Ports":"0.0.0.0:18080->3000/tcp"}
			]`),
	}
}

func TestLoadRuntimeStacksUsesAllProjectsAndContainers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{outputByArgv: runtimeOutputs()}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := loadRuntimeStacks(context.Background(), r, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, runtimeComposeLSArgv()) {
		t.Fatalf("compose ls --all command missing from %#v", argv)
	}
	if !containsArgv(argv, runtimeDockerPSArgv("shop")) {
		t.Fatalf("docker ps -a command missing from %#v", argv)
	}
}

func runtimeComposeLSArgv() string {
	return "docker compose ls --all --format json"
}

func runtimeDockerPSArgv(app string) string {
	return "docker ps -a --filter label=com.docktree.app=" + app + " --format json"
}
