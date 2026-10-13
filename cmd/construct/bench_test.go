package main

import (
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/bench"
)

// A lower latency and more work under CONSTRUCT's settings read as better, a higher one as worse.
func TestBenchTable(t *testing.T) {
	got := benchTable(
		[]bench.Result{{WakeupP99: 2000, ReadP99: 900, VMOps: 100, OOMKills: 2}, {WakeupP99: 2200, ReadP99: 900, VMOps: 100, OOMKills: 2}, {WakeupP99: 1800, ReadP99: 900, VMOps: 100, OOMKills: 2}},
		[]bench.Result{{WakeupP99: 1000, ReadP99: 1800, VMOps: 150}, {WakeupP99: 1100, ReadP99: 1800, VMOps: 150}, {WakeupP99: 900, ReadP99: 1800, VMOps: 150}},
	)
	for _, want := range []string{"-50% better", "+100% worse", "+50% better", "-100% better"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
