package main

import (
	"strings"
	"testing"
)

// A lower latency and more work under CONSTRUCT's settings read as better, a higher one as worse.
func TestBenchTable(t *testing.T) {
	got := benchTable(map[string]map[string][]float64{
		"kernel":    {"wakeup_p99_us": {2000, 2200, 1800}, "read_p99_us": {900}, "vm_ops": {100}, "oom_kills": {2}},
		"construct": {"wakeup_p99_us": {1000, 1100, 900}, "read_p99_us": {1800}, "vm_ops": {150}, "oom_kills": {0}},
	})
	for _, want := range []string{"-50% better", "+100% worse", "+50% better", "-100% better"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
