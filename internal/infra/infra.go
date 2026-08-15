// Package infra manages the per-app shared stack (<app>-infra) and remaps its
// host ports so multiple apps coexist on one machine (F3).
package infra

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type Probe func(port int) bool

func WaitReady(ctx context.Context, hostPorts map[string]int, probe Probe, interval time.Duration) error {
	if probe == nil {
		probe = LocalhostProbe
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		pending := PendingPorts(hostPorts, probe)
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

func LocalhostProbe(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func PendingPorts(hostPorts map[string]int, probe Probe) []string {
	if probe == nil {
		probe = LocalhostProbe
	}
	names := make([]string, 0, len(hostPorts))
	for name := range hostPorts {
		names = append(names, name)
	}
	sort.Strings(names)
	var pending []string
	for _, name := range names {
		if !probe(hostPorts[name]) {
			pending = append(pending, fmt.Sprintf("%s:%d", name, hostPorts[name]))
		}
	}
	return pending
}
