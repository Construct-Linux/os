package main

import (
	"strings"
	"testing"
)

// The kernel's lines while the bench ran go; a service's error then, and the kernel's at another
// time, stay.
func TestWithoutBenchOOM(t *testing.T) {
	errs := strings.Join([]string{
		"[  700.100000] construct kernel: usb 1-1: device descriptor read error",
		"[  787.207970] construct kernel: Out of memory: Killed process 19973 (stress-ng-vm)",
		"[  788.000000] construct sshd-session[19832]: error: mm_reap: child terminated by signal 15",
		"[  791.000000] construct kernel:  oom_kill_process+0x19a/0x210",
	}, "\n")
	got := withoutBenchOOM(errs, [2]float64{780, 800})
	if strings.Contains(got, "Out of memory") || strings.Contains(got, "oom_kill_process") ||
		!strings.Contains(got, "device descriptor") || !strings.Contains(got, "mm_reap") {
		t.Errorf("kept:\n%s", got)
	}
	if withoutBenchOOM(errs, [2]float64{}) != errs {
		t.Error("no bench, yet lines went")
	}
}
