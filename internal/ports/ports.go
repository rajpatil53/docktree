// Package ports computes deterministic, collision-free host ports.
package ports

import (
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"strconv"
)

const ProbeHost = "0.0.0.0"

var hostPortTokenRe = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([0-9]+))?\}$`)

type HostPortToken struct {
	Env     string
	Default int
}

// Offset is fnv1a(slug) % bandWidth.
func Offset(slug string, bandWidth int) int {
	if bandWidth <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(slug))
	return int(h.Sum32() % uint32(bandWidth))
}

// PortFree reports whether a TCP host port can be bound on the address Compose
// publishes by default.
func PortFree(port int) bool {
	return PortFreeOn(ProbeHost, port)
}

func PortFreeOn(host string, port int) bool {
	l, err := net.Listen("tcp4", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// Resolve returns the host port for a service. The canonical candidate is
// base+Offset(slug). If free, it is returned. If taken: fixed services error
// because moving the port breaks external callbacks; others linearly bump within
// the band. free is injected for testability.
func Resolve(base int, slug string, bandWidth int, fixed bool, free func(int) bool) (int, error) {
	cand := base + Offset(slug, bandWidth)
	return resolveFrom(base, cand, bandWidth, fixed, free)
}

// ResolveAfterBindFailure returns the next valid retry port after Compose failed
// to bind the previously-resolved port. This is the residual TOCTOU backstop for
// probe-then-bind races.
func ResolveAfterBindFailure(base, failedPort, bandWidth int, fixed bool, free func(int) bool) (int, error) {
	if fixed {
		return 0, fmt.Errorf("port %d is fixed and failed to bind; free it or reassign", failedPort)
	}
	if failedPort < base || failedPort >= base+bandWidth {
		return 0, fmt.Errorf("failed port %d is outside band [%d,%d)", failedPort, base, base+bandWidth)
	}
	start := failedPort + 1
	if start >= base+bandWidth {
		start = base
	}
	return resolveFrom(base, start, bandWidth, false, func(port int) bool {
		return port != failedPort && freeOrDefault(free, port)
	})
}

func resolveFrom(base, start, bandWidth int, fixed bool, free func(int) bool) (int, error) {
	if bandWidth <= 0 {
		return 0, fmt.Errorf("band width must be positive")
	}
	if start < base || start >= base+bandWidth {
		return 0, fmt.Errorf("port %d is outside band [%d,%d)", start, base, base+bandWidth)
	}
	if fixed {
		if freeOrDefault(free, start) {
			return start, nil
		}
		return 0, fmt.Errorf("port %d is fixed and taken; free it or reassign", start)
	}
	offset := start - base
	for i := 0; i < bandWidth; i++ {
		p := base + ((offset + i) % bandWidth)
		if freeOrDefault(free, p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in band [%d,%d)", base, base+bandWidth)
}

func freeOrDefault(free func(int) bool, port int) bool {
	if free == nil {
		return PortFree(port)
	}
	return free(port)
}

func ParseHostPortToken(published string) (HostPortToken, bool) {
	matches := hostPortTokenRe.FindStringSubmatch(published)
	if matches == nil {
		return HostPortToken{}, false
	}
	var def int
	if matches[2] != "" {
		def, _ = strconv.Atoi(matches[2])
	}
	return HostPortToken{Env: matches[1], Default: def}, true
}
