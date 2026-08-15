package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rajpatil53/docktree/internal/infra"
)

func waitForSharedReadiness(ctx context.Context, r *resolved, deps commandDeps, interval time.Duration, probe infra.Probe) error {
	if len(r.manifest.Shared) == 0 {
		return nil
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		pending, err := pendingSharedServices(ctx, r, deps)
		if err != nil {
			pending = []string{err.Error()}
		}
		if len(pending) == 0 {
			pending = infra.PendingPorts(r.sharedPorts, probe)
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("shared services not ready (%s): %w", strings.Join(pending, ", "), ctx.Err())
		case <-time.After(interval):
		}
	}
}

func pendingSharedServices(ctx context.Context, r *resolved, deps commandDeps) ([]string, error) {
	services, err := composePSState(ctx, r, deps, true)
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, service := range services {
		state := strings.ToLower(service.State)
		health := strings.ToLower(service.Health)
		switch {
		case state != "running":
			byName[service.Name] = strings.TrimSpace(service.Name + ":" + service.State)
		case health != "" && health != "healthy":
			byName[service.Name] = strings.TrimSpace(service.Name + ":" + service.Health)
		default:
			byName[service.Name] = ""
		}
	}
	var pending []string
	for _, name := range r.manifest.Shared {
		status, ok := byName[name]
		if !ok {
			pending = append(pending, name+":missing")
			continue
		}
		if status != "" {
			pending = append(pending, status)
		}
	}
	sort.Strings(pending)
	return pending, nil
}
