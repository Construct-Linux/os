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

// Each error comes with the journal's lines from five seconds before it to two after, windows
// apart split by "--": what was killed, or what failed first, is in them.
func TestAroundErrors(t *testing.T) {
	journal := strings.Join([]string{
		"[    2.000000] construct systemd[1]: Started early.",
		"[    8.500000] construct systemd[1]: gdm.service: Killing process 812 (gdm) with signal SIGKILL.",
		"[   13.140042] construct kernel: erofs (device dm-0): failed to read inode meta block (nid: 99033472): -4",
		"[   14.000000] construct systemd[1]: Started late.",
		"[   30.000000] construct systemd[1]: Unrelated.",
		"[   40.000000] construct kernel: second error",
		"-- Boot b1 --",
	}, "\n")
	errs := "[   13.140042] construct kernel: erofs (device dm-0): failed to read inode meta block (nid: 99033472): -4\n" +
		"[   40.000000] construct kernel: second error"
	want := "[    8.500000] construct systemd[1]: gdm.service: Killing process 812 (gdm) with signal SIGKILL.\n" +
		"[   13.140042] construct kernel: erofs (device dm-0): failed to read inode meta block (nid: 99033472): -4\n" +
		"[   14.000000] construct systemd[1]: Started late.\n" +
		"--\n" +
		"[   40.000000] construct kernel: second error\n"
	if got := aroundErrors(journal, errs, 5, 2); got != want {
		t.Errorf("aroundErrors =\n%s\nwant\n%s", got, want)
	}
}
