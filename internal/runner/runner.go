// Package runner provides fakeable process execution for Docktree commands.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Command struct {
	Argv   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Runner interface {
	Run(context.Context, Command) error
	Output(context.Context, Command) ([]byte, error)
}

type Exec struct{}

func (Exec) Run(ctx context.Context, command Command) error {
	cmd, err := buildExec(ctx, command)
	if err != nil {
		return err
	}
	return cmd.Run()
}

func (Exec) Output(ctx context.Context, command Command) ([]byte, error) {
	var stdout bytes.Buffer
	command.Stdout = &stdout
	cmd, err := buildExec(ctx, command)
	if err != nil {
		return nil, err
	}
	err = cmd.Run()
	return stdout.Bytes(), err
}

func ComposeArgv(project, envFile, composeFile, action string, args ...string) []string {
	argv := []string{
		"docker", "compose",
	}
	if action == "up" {
		argv = append(argv, "--progress", "plain")
	}
	argv = append(argv,
		"--env-file", envFile,
		"-p", project,
		"-f", composeFile,
		action,
	)
	return append(argv, args...)
}

// WithEnvOverrides layers KEY=VALUE pairs over the environment the executed
// process sees, so they win over both the ambient shell env and a secrets
// wrapper's injected values. Pairs are appended to Env (os/exec gives the
// last duplicate of a key precedence). A wrapper injects its own values
// after Env is applied, so when one is configured the pairs are also
// interposed via env(1) ahead of the argv; apply WithSecretsWrapper after
// this so the interposer lands between the wrapper and the wrapped command.
// The "--" guards against keys env(1) would otherwise parse as options.
func WithEnvOverrides(wrapper string, overrides [][2]string, command Command) Command {
	if len(overrides) == 0 {
		return command
	}
	pairs := make([]string, 0, len(overrides))
	for _, kv := range overrides {
		pairs = append(pairs, kv[0]+"="+kv[1])
	}
	command.Env = append(append([]string{}, command.Env...), pairs...)
	if len(strings.Fields(wrapper)) > 0 {
		command.Argv = append(append([]string{"env", "--"}, pairs...), command.Argv...)
	}
	return command
}

func WithSecretsWrapper(wrapper string, command Command) Command {
	fields := strings.Fields(wrapper)
	if len(fields) == 0 {
		return command
	}
	command.Argv = append(append([]string{}, fields...), command.Argv...)
	return command
}

func PreflightSecrets(ctx context.Context, r Runner, base Command, wrapper string, interactive bool) error {
	if strings.TrimSpace(wrapper) == "" || interactive {
		return nil
	}
	base.Argv = []string{"true"}
	if err := r.Run(ctx, WithSecretsWrapper(wrapper, base)); err != nil {
		return fmt.Errorf("secrets wrapper preflight failed: %w", err)
	}
	return nil
}

func buildExec(ctx context.Context, command Command) (*exec.Cmd, error) {
	if len(command.Argv) == 0 {
		return nil, fmt.Errorf("empty command argv")
	}
	cmd := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = command.Dir
	if len(command.Env) > 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}
	cmd.Stdin = command.Stdin
	cmd.Stdout = command.Stdout
	cmd.Stderr = command.Stderr
	return cmd, nil
}
