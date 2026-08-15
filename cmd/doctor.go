package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/dockerstate"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/runner"
)

type diagnostic struct {
	severity string
	code     string
	message  string
}

type servicePortKey struct {
	project string
	service string
}

func Doctor(args []string) error {
	return runDoctor(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runDoctor(ctx context.Context, args []string, deps commandDeps) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: docktree doctor")
	}
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	cfg, err := config.LoadFile(paths.ConfigFile(wd))
	if err != nil {
		return err
	}
	r, err := resolveWithFree(wd, deps.portFree)
	if err != nil {
		return err
	}

	var issues []diagnostic
	if _, err := deps.runner.Output(ctx, dockerInfoCommand(r, deps)); err != nil {
		issues = appendIssue(issues, "error", "daemon_unavailable", fmt.Sprintf("Docker daemon unavailable: %v", err))
	}
	base := runner.Command{
		Dir:    wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	if err := runner.PreflightSecrets(ctx, deps.runner, base, cfg.Secrets.Wrapper, isInteractive(deps.stdin)); err != nil {
		issues = appendIssue(issues, "error", "secrets_preflight", err.Error())
	}
	for _, warning := range cfg.Warnings {
		issues = appendIssue(issues, "warning", "config_unknown_key", warning.Message)
	}

	model, err := loadComposeModel(ctx, wd, cfg, deps)
	if err != nil {
		issues = appendIssue(issues, "error", "compose_config", fmt.Sprintf("docker compose config failed: %v", err))
	} else {
		resolvedWithModel, err := resolveWithModel(wd, model, deps.portFree)
		if err != nil {
			issues = appendIssue(issues, "error", "port_resolution", err.Error())
		} else {
			r = resolvedWithModel
		}
		issues = append(issues, composeDiagnostics(model, r)...)
	}

	if len(r.manifest.Shared) > 0 {
		infra, err := composePSState(ctx, r, deps, true)
		if err != nil {
			issues = appendIssue(issues, "error", "infra_status", fmt.Sprintf("could not inspect infra: %v", err))
		} else {
			issues = append(issues, infraDiagnostics(r, infra)...)
		}
	}
	stacks, err := loadRuntimeStacks(ctx, r, deps)
	if err != nil {
		issues = appendIssue(issues, "error", "docker_state", fmt.Sprintf("could not inspect containers: %v", err))
	} else {
		issues = append(issues, portDriftDiagnostics(r, stacks)...)
	}

	if len(issues) == 0 {
		fmt.Fprintln(deps.stdout, "doctor: ok")
		return nil
	}
	errors := 0
	for _, issue := range issues {
		if issue.severity == "error" {
			errors++
		}
		fmt.Fprintf(deps.stdout, "%s %s: %s\n", issue.severity, issue.code, issue.message)
	}
	if errors > 0 {
		return fmt.Errorf("doctor found %d error(s)", errors)
	}
	return nil
}

func composeDiagnostics(model *compose.Model, r *resolved) []diagnostic {
	var issues []diagnostic
	options := composeOptions(r)
	for _, issue := range compose.Validate(model, options) {
		severity := "warning"
		if issue.Severity == compose.Error {
			severity = "error"
		}
		issues = appendIssue(issues, severity, issue.Code, issue.Message)
	}
	for _, shared := range r.manifest.Shared {
		if _, ok := model.Services[shared]; !ok {
			issues = appendIssue(issues, "error", "shared_service_missing", fmt.Sprintf("shared service %q is listed in docktree.toml but not present in compose config", shared))
		}
	}
	return issues
}

func infraDiagnostics(r *resolved, services []dockerstate.Service) []diagnostic {
	if len(r.manifest.Shared) == 0 {
		return nil
	}
	byName := map[string]dockerstate.Service{}
	for _, service := range services {
		byName[service.Name] = service
	}
	var issues []diagnostic
	for _, name := range r.manifest.Shared {
		service, ok := byName[name]
		if !ok {
			issues = appendIssue(issues, "error", "infra_service_missing", fmt.Sprintf("shared service %q is not running in %s", name, r.names.InfraProject))
			continue
		}
		if service.State != "" && service.State != "running" {
			issues = appendIssue(issues, "error", "infra_unhealthy", fmt.Sprintf("shared service %q state is %s", name, service.State))
		}
		if strings.EqualFold(service.Health, "unhealthy") {
			issues = appendIssue(issues, "error", "infra_unhealthy", fmt.Sprintf("shared service %q health is unhealthy", name))
		}
	}
	return issues
}

func portDriftDiagnostics(r *resolved, stacks []dockerstate.Stack) []diagnostic {
	expected := map[servicePortKey]int{}
	for name, port := range r.servicePorts {
		expected[servicePortKey{project: r.names.Project, service: name}] = port
	}
	for name, port := range r.sharedPorts {
		expected[servicePortKey{project: r.names.InfraProject, service: name}] = port
	}
	if len(expected) == 0 {
		return nil
	}
	actual := actualPorts(stacks)
	seen := actualServices(stacks)
	var issues []diagnostic
	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			if seen[key] {
				issues = appendIssue(issues, "warning", "port_missing", fmt.Sprintf("service %q in project %q expected localhost:%d but Docker publishes no host port", key.service, key.project, want))
			}
			continue
		}
		if got == want {
			continue
		}
		issues = appendIssue(issues, "warning", "port_drift", fmt.Sprintf("service %q in project %q expected localhost:%d but Docker publishes localhost:%d", key.service, key.project, want, got))
	}
	return issues
}

func actualPorts(stacks []dockerstate.Stack) map[servicePortKey]int {
	out := map[servicePortKey]int{}
	for _, stack := range stacks {
		for _, service := range stack.Services {
			project := service.Project
			if project == "" {
				project = stack.Project
			}
			for _, port := range service.Ports {
				if port.Published > 0 {
					out[servicePortKey{project: project, service: service.Name}] = port.Published
					break
				}
			}
		}
	}
	return out
}

func actualServices(stacks []dockerstate.Stack) map[servicePortKey]bool {
	out := map[servicePortKey]bool{}
	for _, stack := range stacks {
		for _, service := range stack.Services {
			project := service.Project
			if project == "" {
				project = stack.Project
			}
			out[servicePortKey{project: project, service: service.Name}] = true
		}
	}
	return out
}

func appendIssue(issues []diagnostic, severity, code, message string) []diagnostic {
	return append(issues, diagnostic{severity: severity, code: code, message: message})
}
