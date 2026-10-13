package bench

import (
	"os"
	"path/filepath"
	"testing"
)

// The kernel's profile, applied and taken back: what is current is read as written, the bracketed
// choice of a list included, and restored as it was.
func TestApplyAndRestore(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"proc/sys/vm/swappiness":                    "150",
		"proc/sys/vm/page-cluster":                  "0",
		"proc/sys/vm/vfs_cache_pressure":            "50",
		"proc/sys/vm/dirty_ratio":                   "0",
		"proc/sys/vm/dirty_background_ratio":        "0",
		"proc/sys/vm/dirty_bytes":                   "268435456",
		"proc/sys/vm/dirty_background_bytes":        "67108864",
		"proc/sys/vm/dirty_writeback_centisecs":     "1500",
		"proc/sys/kernel/nmi_watchdog":              "0",
		"proc/sys/net/ipv4/tcp_congestion_control":  "bbr",
		"sys/kernel/mm/transparent_hugepage/defrag": "always defer [defer+madvise] madvise never",
		"sys/module/zswap/parameters/enabled":       "N",
		"sys/block/nvme0n1/queue/scheduler":         "mq-deadline [kyber] none",
	}
	for rel, v := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kernel := Kernel([]string{"nvme0n1"})
	construct, err := Current(root, kernel, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, kernel); err != nil {
		t.Fatal(err)
	}
	if v, _ := read(filepath.Join(root, "sys/block/nvme0n1/queue/scheduler")); v != "none" {
		t.Errorf("NVMe scheduler under the kernel's profile: %q", v)
	}
	if err := Apply(root, construct); err != nil {
		t.Fatal(err)
	}
	// CONSTRUCT's dirty limits are bytes, its ratios reading 0: the bytes are what come back, never a
	// 0 ratio that would clear them.
	for _, s := range construct.Settings {
		if s.Path == "proc/sys/vm/dirty_ratio" || s.Path == "proc/sys/vm/dirty_background_ratio" {
			t.Errorf("CONSTRUCT's profile writes %s=%s, which clears its bytes", s.Path, s.Value)
		}
	}
	for rel, want := range map[string]string{"proc/sys/vm/swappiness": "150", "sys/kernel/mm/transparent_hugepage/defrag": "defer+madvise", "sys/block/nvme0n1/queue/scheduler": "kyber",
		"proc/sys/vm/dirty_bytes": "268435456", "proc/sys/vm/dirty_background_bytes": "67108864"} {
		if v, _ := read(filepath.Join(root, rel)); v != want {
			t.Errorf("%s restored as %q, want %q", rel, v, want)
		}
	}
}

func TestParsers(t *testing.T) {
	sch := []byte(`{"normal": {"version": "7.2.8"},"int": {"time": 1, "wakeup_latency_pct50.0": 9, "wakeup_latency_pct99.0": 1834, "wakeup_latency_min": 1,"wakeup_latency_max": 9000}}`)
	if v, err := SchbenchWakeupP99(sch); err != nil || v != 1834 {
		t.Errorf("schbench: %v, %v", v, err)
	}
	fio := []byte(`{"jobs": [{"jobname": "writer", "read": {"clat_ns": {}}}, {"jobname": "reader", "read": {"clat_ns": {"percentile": {"50.000000": 90000, "99.000000": 2457600}}}}]}`)
	if v, err := FioReadP99(fio, "reader"); err != nil || v != 2457.6 {
		t.Errorf("fio: %v, %v", v, err)
	}
	yaml := []byte("system-info:\n    run-by: root\nmetrics:\n    - stressor: vm\n      bogo-ops: 1200\n      bogo-ops-per-second-usr-sys-time: 30.5\n      bogo-ops-per-second-real-time: 20.25\n")
	if v, err := StressVMRate(yaml); err != nil || v != 20.25 {
		t.Errorf("stress-ng: %v, %v", v, err)
	}
	if m := Median([]float64{5, 1, 3, 9}); m != 4 {
		t.Errorf("median %v", m)
	}
}

// A setting the kernel has not got is left out, and named.
func TestWritableDropsWhatIsMissing(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "proc/sys/vm/swappiness")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("150"), 0o644); err != nil {
		t.Fatal(err)
	}
	kept, dropped := Writable(root, Profile{Settings: []Setting{{"proc/sys/vm/swappiness", "60"}, {"proc/sys/kernel/nmi_watchdog", "1"}}})
	if len(kept.Settings) != 1 || len(dropped) != 1 || dropped[0] != "proc/sys/kernel/nmi_watchdog" {
		t.Errorf("kept %+v, dropped %v", kept.Settings, dropped)
	}
}
