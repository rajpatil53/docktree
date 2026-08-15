package infra

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWaitReadyUsesFakeablePortProbe(t *testing.T) {
	calls := 0
	err := WaitReady(context.Background(), map[string]int{"postgres": 5432}, func(port int) bool {
		calls++
		return port == 5432
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("probe was not called")
	}
}

func TestWaitReadyReturnsContextErrorWithServiceName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := WaitReady(ctx, map[string]int{"postgres": 5432}, func(int) bool { return false }, time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady returned nil, want context failure")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error = %v, want service name", err)
	}
}
