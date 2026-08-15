package compose

import (
	"fmt"

	"github.com/rajpatil53/docktree/internal/ports"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type Issue struct {
	Severity Severity
	Code     string
	Service  string
	Resource string
	Message  string
}

func Validate(model *Model, opts Options) []Issue {
	opts = opts.withDefaults()
	shared := sharedSet(opts.Shared)
	var issues []Issue

	for name, service := range model.Services {
		if shared[name] {
			for dep := range service.DependsOn {
				if !shared[dep] {
					issues = append(issues, Issue{
						Severity: Error,
						Code:     "shared_depends_on_non_shared",
						Service:  name,
						Resource: dep,
						Message:  fmt.Sprintf("shared service %q depends on non-shared service %q", name, dep),
					})
				}
			}
		}
		if service.ContainerName != "" {
			issues = append(issues, Issue{
				Severity: Warning,
				Code:     "container_name",
				Service:  name,
				Resource: service.ContainerName,
				Message:  fmt.Sprintf("service %q sets fixed container_name %q", name, service.ContainerName),
			})
		}
		for _, port := range service.Ports {
			if port.Published == "" {
				continue
			}
			if _, ok := ports.ParseHostPortToken(port.Published); !ok {
				issues = append(issues, Issue{
					Severity: Warning,
					Code:     "hardcoded_host_port",
					Service:  name,
					Resource: port.Published,
					Message:  fmt.Sprintf("service %q publishes host port %s without a Docktree port token", name, port.Published),
				})
			}
		}
	}
	for name, serviceOpts := range opts.Services {
		if serviceOpts.HostPortEnv == "" {
			continue
		}
		service, ok := model.Services[name]
		if !ok {
			issues = append(issues, Issue{
				Severity: Error,
				Code:     "missing_host_port_token",
				Service:  name,
				Resource: serviceOpts.HostPortEnv,
				Message:  fmt.Sprintf("service %q configures host_port_env %q but is not present in compose config", name, serviceOpts.HostPortEnv),
			})
			continue
		}
		found := false
		for _, port := range service.Ports {
			token, ok := ports.ParseHostPortToken(port.Published)
			if ok && token.Env == serviceOpts.HostPortEnv {
				found = true
				break
			}
		}
		if !found {
			issues = append(issues, Issue{
				Severity: Error,
				Code:     "missing_host_port_token",
				Service:  name,
				Resource: serviceOpts.HostPortEnv,
				Message:  fmt.Sprintf("service %q configures host_port_env %q but compose does not publish that token", name, serviceOpts.HostPortEnv),
			})
		}
	}

	for name, volume := range model.Volumes {
		if volume.Name != "" {
			issues = append(issues, Issue{
				Severity: Warning,
				Code:     "named_volume",
				Resource: name,
				Message:  fmt.Sprintf("volume %q uses explicit name %q", name, volume.Name),
			})
		}
		if volume.External {
			issues = append(issues, Issue{
				Severity: Warning,
				Code:     "external_volume",
				Resource: name,
				Message:  fmt.Sprintf("volume %q is external", name),
			})
		}
	}
	return issues
}
