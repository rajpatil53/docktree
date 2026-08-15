package ports

import (
	"hash/fnv"
	"net"
	"testing"
)

func TestOffsetUsesFNV1a(t *testing.T) {
	slug := "feature_one"
	h := fnv.New32a()
	_, _ = h.Write([]byte(slug))
	want := int(h.Sum32() % 1000)

	if got := Offset(slug, 1000); got != want {
		t.Fatalf("Offset = %d, want FNV-1a offset %d", got, want)
	}
}

func TestPortFreeProbesAllInterfaces(t *testing.T) {
	if !PortFreeOn(ProbeHost, 0) {
		t.Fatalf("PortFreeOn should bind %s:0", ProbeHost)
	}

	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot bind all-interface port")
	}
	defer l.Close()
	bound := l.Addr().(*net.TCPAddr).Port
	if PortFree(bound) {
		t.Fatalf("PortFree(%d) said free but it is bound on 0.0.0.0", bound)
	}
}

func TestResolveReturnsCanonicalWhenFree(t *testing.T) {
	base := 20000
	p, err := Resolve(base, "feature_one", 1000, false, isFreeAlways)
	if err != nil {
		t.Fatal(err)
	}
	if p != base+Offset("feature_one", 1000) {
		t.Fatalf("Resolve = %d, want base+FNV offset", p)
	}
}

func TestResolveBumpsNonFixedWhenTaken(t *testing.T) {
	base := 20000
	candidate := base + Offset("feature_one", 1000)
	taken := map[int]bool{candidate: true}
	p, err := Resolve(base, "feature_one", 1000, false, func(port int) bool { return !taken[port] })
	if err != nil {
		t.Fatal(err)
	}
	if p != candidate+1 {
		t.Fatalf("Resolve bumped to %d, want %d", p, candidate+1)
	}
}

func TestResolveFixedFailsWhenTaken(t *testing.T) {
	base := 20000
	candidate := base + Offset("feature_one", 1000)
	taken := map[int]bool{candidate: true}
	_, err := Resolve(base, "feature_one", 1000, true, func(port int) bool { return !taken[port] })
	if err == nil {
		t.Fatal("expected an actionable error for a fixed-service port collision, got nil")
	}
}

func TestResolveExhaustedBand(t *testing.T) {
	_, err := Resolve(20000, "feature_one", 3, false, func(int) bool { return false })
	if err == nil {
		t.Fatal("expected exhausted band error")
	}
}

func TestResolveAfterBindFailureInputs(t *testing.T) {
	p, err := ResolveAfterBindFailure(20000, 20002, 3, false, isFreeAlways)
	if err != nil {
		t.Fatal(err)
	}
	if p != 20000 {
		t.Fatalf("retry wrapped to %d, want 20000", p)
	}

	if _, err := ResolveAfterBindFailure(20000, 20002, 3, true, isFreeAlways); err == nil {
		t.Fatal("fixed service should not retry after bind failure")
	}
	if _, err := ResolveAfterBindFailure(20000, 19999, 3, false, isFreeAlways); err == nil {
		t.Fatal("out-of-band failed port should be rejected")
	}
}

func TestParseHostPortToken(t *testing.T) {
	token, ok := ParseHostPortToken("${API_PORT:-3000}")
	if !ok || token.Env != "API_PORT" || token.Default != 3000 {
		t.Fatalf("token = %#v ok=%v", token, ok)
	}
	if _, ok := ParseHostPortToken("3000"); ok {
		t.Fatal("hardcoded port should not parse as a token")
	}
}

func isFreeAlways(int) bool { return true }
