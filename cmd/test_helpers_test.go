package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/runner"
)

const basicComposeConfigJSON = `{
  "services": {
    "api": {
      "depends_on": {"postgres": {"condition": "service_healthy", "required": true}},
      "ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]
    },
    "postgres": {
      "ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]
    }
  }
}`

type fakeRunner struct {
	commands              []runner.Command
	configOutput          []byte
	infraPSOutput         []byte
	networkMissing        bool
	allowUnexpectedOutput bool
	outputByArgv          map[string][]byte
	outputErrByArgv       map[string]error
	runErrByArgv          map[string]error
	runErrOnceByArgv      map[string]error
	runErrOnceBySubstr    map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, cmd runner.Command) error {
	f.commands = append(f.commands, cmd)
	joined := strings.Join(cmd.Argv, " ")
	if err := f.runErrOnceByArgv[joined]; err != nil {
		delete(f.runErrOnceByArgv, joined)
		return err
	}
	for substr, err := range f.runErrOnceBySubstr {
		if strings.Contains(joined, substr) {
			delete(f.runErrOnceBySubstr, substr)
			return err
		}
	}
	if err := f.runErrByArgv[joined]; err != nil {
		return err
	}
	if len(cmd.Argv) >= 3 && cmd.Argv[0] == "docker" && cmd.Argv[1] == "network" && cmd.Argv[2] == "inspect" && f.networkMissing {
		return errors.New("network missing")
	}
	return nil
}

func (f *fakeRunner) Output(ctx context.Context, cmd runner.Command) ([]byte, error) {
	f.commands = append(f.commands, cmd)
	joined := strings.Join(cmd.Argv, " ")
	if err := f.outputErrByArgv[joined]; err != nil {
		return nil, err
	}
	if out, ok := f.outputByArgv[joined]; ok {
		return out, nil
	}
	switch {
	case strings.Contains(joined, " config ") && strings.Contains(joined, "--format json"):
		return f.configOutput, nil
	case strings.Contains(joined, " ps --status running --services"):
		return f.infraPSOutput, nil
	default:
		if f.allowUnexpectedOutput {
			return nil, nil
		}
		return nil, errors.New("unexpected output command: " + joined)
	}
}

func newCommandTestDeps(root string, fr *fakeRunner, events *[]string) commandDeps {
	deps := defaultCommandDeps(strings.NewReader(""), io.Discard, io.Discard)
	deps.cwd = func() (string, error) { return root, nil }
	deps.runner = fr
	deps.portFree = func(int) bool { return true }
	deps.waitShared = func(ctx context.Context, r *resolved) error {
		*events = append(*events, "wait-shared")
		return nil
	}
	deps.event = func(name string) {
		*events = append(*events, name)
	}
	return deps
}

func writeBasicProject(t *testing.T, root string) {
	t.Helper()
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

[services.postgres]
host_port_env = "POSTGRES_PORT"
`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandArgvStrings(commands []runner.Command) []string {
	out := make([]string, 0, len(commands))
	for _, cmd := range commands {
		out = append(out, strings.Join(cmd.Argv, " "))
	}
	return out
}

func assertContainsInOrder(t *testing.T, got string, wants []string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		index := strings.Index(got[offset:], want)
		if index < 0 {
			t.Fatalf("output missing %q after offset %d:\n%s", want, offset, got)
		}
		offset += index + len(want)
	}
}

func indexArgv(argv []string, want string) int {
	for i, got := range argv {
		if got == want {
			return i
		}
	}
	return -1
}

func forkVolumeListArgv(app string) string {
	return "docker volume ls --filter label=com.docktree.app=" + app + " --filter label=com.docktree.fork --format json"
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
