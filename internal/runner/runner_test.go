package runner

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestComposeArgvBuildsProjectEnvFileAndComposeFileArgs(t *testing.T) {
	got := ComposeArgv("shop-feature", ".docktree/.env.worktree", ".docktree/compose.worktree.yml", "up", "-d", "api")
	want := []string{
		"docker", "compose",
		"--progress", "plain",
		"--env-file", ".docktree/.env.worktree",
		"-p", "shop-feature",
		"-f", ".docktree/compose.worktree.yml",
		"up", "-d", "api",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ComposeArgv = %#v, want %#v", got, want)
	}
}

func TestComposeArgvLeavesNonUpCommandsUnchanged(t *testing.T) {
	got := ComposeArgv("shop-feature", ".docktree/.env.worktree", ".docktree/compose.worktree.yml", "ps")
	want := []string{
		"docker", "compose",
		"--env-file", ".docktree/.env.worktree",
		"-p", "shop-feature",
		"-f", ".docktree/compose.worktree.yml",
		"ps",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ComposeArgv = %#v, want %#v", got, want)
	}
}

func TestExecRunnerUsesWorkingDirectoryAndProcessEnv(t *testing.T) {
	dir := t.TempDir()
	out, err := Exec{}.Output(context.Background(), Command{
		Argv: []string{"sh", "-c", "printf '%s|%s' \"$PWD\" \"$DOCKTREE_TEST\""},
		Dir:  dir,
		Env:  []string{"DOCKTREE_TEST=ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := wantDir + "|ok"
	if string(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestExecRunnerRunAndEmptyArgvErrors(t *testing.T) {
	if err := (Exec{}).Run(context.Background(), Command{Argv: []string{"true"}}); err != nil {
		t.Fatalf("Run(true) = %v", err)
	}
	if err := (Exec{}).Run(context.Background(), Command{}); err == nil || !strings.Contains(err.Error(), "empty command argv") {
		t.Fatalf("empty argv error = %v", err)
	}
}

func TestWithSecretsWrapperPrefixesCommandArgv(t *testing.T) {
	got := WithSecretsWrapper("doppler run --", Command{Argv: []string{"docker", "compose", "up", "-d"}})
	want := []string{"doppler", "run", "--", "docker", "compose", "up", "-d"}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("wrapped argv = %#v, want %#v", got.Argv, want)
	}

	bare := WithSecretsWrapper("   ", Command{Argv: []string{"docker"}})
	if !reflect.DeepEqual(bare.Argv, []string{"docker"}) {
		t.Fatalf("empty wrapper should leave argv unchanged: %#v", bare.Argv)
	}
}

func TestWithEnvOverridesAppendsPairsToCommandEnv(t *testing.T) {
	command := Command{
		Argv: []string{"docker", "compose", "up", "-d"},
		Env:  []string{"AMBIENT=1"},
	}
	got := WithEnvOverrides("", [][2]string{{"FOO", "bar"}, {"BAZ", "has space $x"}}, command)
	wantEnv := []string{"AMBIENT=1", "FOO=bar", "BAZ=has space $x"}
	if !reflect.DeepEqual(got.Env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", got.Env, wantEnv)
	}
	if !reflect.DeepEqual(got.Argv, command.Argv) {
		t.Fatalf("argv without wrapper should be unchanged: %#v", got.Argv)
	}
}

func TestWithEnvOverridesInterposesEnvUtilityUnderWrapper(t *testing.T) {
	command := Command{Argv: []string{"docker", "compose", "up", "-d"}}
	got := WithSecretsWrapper("doppler run --",
		WithEnvOverrides("doppler run --", [][2]string{{"FOO", "bar"}, {"BAZ", "q v"}}, command))
	want := []string{
		"doppler", "run", "--",
		"env", "--", "FOO=bar", "BAZ=q v",
		"docker", "compose", "up", "-d",
	}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", got.Argv, want)
	}
	wantEnv := []string{"FOO=bar", "BAZ=q v"}
	if !reflect.DeepEqual(got.Env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", got.Env, wantEnv)
	}
}

func TestWithEnvOverridesIsNoopWithoutOverrides(t *testing.T) {
	command := Command{Argv: []string{"docker", "compose", "ps"}, Env: []string{"AMBIENT=1"}}
	for _, wrapper := range []string{"", "doppler run --"} {
		got := WithEnvOverrides(wrapper, nil, command)
		if !reflect.DeepEqual(got, command) {
			t.Fatalf("wrapper %q: command changed: %#v", wrapper, got)
		}
	}
}

func TestPreflightSecretsFailsFastForNonInteractiveWrapper(t *testing.T) {
	rec := &recordingRunner{err: errors.New("not logged in")}
	err := PreflightSecrets(context.Background(), rec, Command{Dir: "/repo"}, "doppler run --", false)
	if err == nil {
		t.Fatal("PreflightSecrets returned nil, want failure")
	}
	if !strings.Contains(err.Error(), "secrets wrapper preflight failed") {
		t.Fatalf("error = %v", err)
	}
	want := []string{"doppler", "run", "--", "true"}
	if len(rec.commands) != 1 || !reflect.DeepEqual(rec.commands[0].Argv, want) {
		t.Fatalf("preflight commands = %#v, want %v", rec.commands, want)
	}
}

type recordingRunner struct {
	err      error
	commands []Command
}

func (r *recordingRunner) Run(ctx context.Context, cmd Command) error {
	r.commands = append(r.commands, cmd)
	return r.err
}

func (r *recordingRunner) Output(ctx context.Context, cmd Command) ([]byte, error) {
	r.commands = append(r.commands, cmd)
	return nil, r.err
}
