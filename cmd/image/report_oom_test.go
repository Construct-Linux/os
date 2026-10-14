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

// PipeWire's lines while the screen was recorded go; its lines at another time, and anything
// else's then, stay.
func TestWithoutCastFormats(t *testing.T) {
	errs := strings.Join([]string{
		"[   60.000000] construct pipewire[988]: pw.context: params Spa:Enum:ParamId:EnumFormat: 0:0 Invalid argument (input format (no more input formats))",
		"[   79.940162] construct pipewire[988]: pw.context: params Spa:Enum:ParamId:EnumFormat: 0:0 Invalid argument (input format (no more input formats))",
		"[   79.940193] construct pipewire[988]: pw.context: Object: size 272, type Spa:Pod:Object:Param:Format (262147), id Spa:Enum:ParamId:EnumFormat (3)",
		"[   79.940205] construct pipewire[988]: pw.context:   Prop: key Spa:Pod:Object:Param:Format:mediaType (1), flags 00000000",
		"[   80.000000] construct pipewire[988]: pw.link: (67.0.0 -> 64.0.0) negotiating -> error no more input formats (-22)",
		"[   80.100000] construct gnome-shell[665]: JS ERROR: TypeError: x is null",
		"[   90.000000] construct pipewire[988]: mod.rt: could not make thread realtime",
	}, "\n")
	got := withoutCastFormats(errs, [2]float64{79, 83})
	if strings.Contains(got, "79.94") || strings.Contains(got, "pw.link") || !strings.Contains(got, "60.000000") || !strings.Contains(got, "mod.rt") || !strings.Contains(got, "JS ERROR") {
		t.Errorf("kept:\n%s", got)
	}
	if withoutCastFormats(errs, [2]float64{}) != errs {
		t.Error("no recording, yet lines went")
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
