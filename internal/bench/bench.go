// Package bench measures what CONSTRUCT tunes against what the kernel would do by itself, on the
// machine it runs on: the same workloads under two profiles, alternated, so neither the hour nor
// the machine's warmth picks a winner. A profile is the settings it writes; the workloads' numbers
// are read from schbench, fio and stress-ng, whose output this package parses.
package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Setting is one value a profile writes, a file under /proc/sys or /sys.
type Setting struct {
	Path, Value string
}

// Profile is a set of settings, and whether swap is CONSTRUCT's zram.
type Profile struct {
	Name     string
	Settings []Setting
	Zram     bool
}

// Kernel is the kernel's own defaults for everything CONSTRUCT tunes (usr/lib/sysctl.d/70-construct
// .conf, zram-generator.conf, the I/O scheduler and THP rules): what a machine would do untuned.
// disks are the block devices to set the scheduler of, by name (nvme0n1, sda).
func Kernel(disks []string) Profile {
	p := Profile{Name: "kernel", Settings: []Setting{
		{"proc/sys/vm/swappiness", "60"},
		{"proc/sys/vm/page-cluster", "3"},
		{"proc/sys/vm/vfs_cache_pressure", "100"},
		// Writing a ratio clears the bytes setting, as the kernel's defaults have it.
		{"proc/sys/vm/dirty_ratio", "20"},
		{"proc/sys/vm/dirty_background_ratio", "10"},
		{"proc/sys/vm/dirty_writeback_centisecs", "500"},
		{"proc/sys/kernel/nmi_watchdog", "1"},
		{"proc/sys/net/ipv4/tcp_congestion_control", "cubic"},
		{"sys/kernel/mm/transparent_hugepage/defrag", "madvise"},
		{"sys/module/zswap/parameters/enabled", "Y"},
	}}
	for _, name := range disks {
		// The kernel's own choice for a multi-queue device: none on NVMe, mq-deadline otherwise.
		sched := "mq-deadline"
		if strings.HasPrefix(name, "nvme") {
			sched = "none"
		}
		p.Settings = append(p.Settings, Setting{filepath.Join("sys/block", name, "queue/scheduler"), sched})
	}
	return p
}

// bytesTwin is the bytes setting of each dirty ratio. The kernel keeps one of a pair: writing
// either clears the other, and the one not in use reads 0. CONSTRUCT sets the bytes
// (70-construct.conf), so its ratios read 0, and writing such a 0 back would clear its limits.
var bytesTwin = map[string]string{
	"proc/sys/vm/dirty_ratio":            "proc/sys/vm/dirty_bytes",
	"proc/sys/vm/dirty_background_ratio": "proc/sys/vm/dirty_background_bytes",
}

// active is the setting at path as the kernel uses it: the path, or its bytes twin when that is
// the one set, and its value.
func active(root, path string) (string, string, error) {
	v, err := read(filepath.Join(root, path))
	if twin, ok := bytesTwin[path]; ok && err == nil && v == "0" {
		if tv, terr := read(filepath.Join(root, twin)); terr == nil && tv != "0" {
			return twin, tv, nil
		}
	}
	return path, v, err
}

// Current is what the machine has now, for the settings of p: CONSTRUCT's own profile when the
// machine runs CONSTRUCT, and what is put back when the bench ends.
func Current(root string, p Profile, zram bool) (Profile, error) {
	cur := Profile{Name: "construct", Zram: zram}
	for _, s := range p.Settings {
		path, v, err := active(root, s.Path)
		if err != nil {
			return Profile{}, err
		}
		cur.Settings = append(cur.Settings, Setting{path, v})
	}
	return cur, nil
}

// Writable is p without the settings this machine's kernel has not got or refuses: a VM has no
// NMI watchdog, a kernel without zswap no zswap switch. Each is tried by writing back what it holds.
// It answers the settings it dropped, for the person to see what was not compared.
func Writable(root string, p Profile) (Profile, []string) {
	kept := Profile{Name: p.Name, Zram: p.Zram}
	var dropped []string
	for _, s := range p.Settings {
		rel, v, err := active(root, s.Path)
		if err == nil {
			err = os.WriteFile(filepath.Join(root, rel), []byte(v), 0o644)
		}
		if err != nil {
			dropped = append(dropped, s.Path)
			continue
		}
		kept.Settings = append(kept.Settings, s)
	}
	return kept, dropped
}

// Apply writes p's settings under root. Use (run.go) turns zram on or off with them: that is
// systemd's units', not a setting's.
func Apply(root string, p Profile) error {
	var errs []error
	for _, s := range p.Settings {
		if err := os.WriteFile(filepath.Join(root, s.Path), []byte(s.Value), 0o644); err != nil {
			errs = append(errs, fmt.Errorf("%s=%s: %w", s.Path, s.Value, err))
		}
	}
	return errors.Join(errs...)
}

// read is a setting's value as written back: the selected one of a list the kernel shows with
// brackets ("always [madvise] never", "[mq-deadline] kyber none").
func read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if i := strings.Index(v, "["); i >= 0 {
		if j := strings.Index(v[i:], "]"); j > 0 {
			return v[i+1 : i+j], nil
		}
	}
	return v, nil
}

// SchbenchWakeupP99 is the 99th percentile of schbench's wakeup latency, in microseconds, from its
// JSON (-j): how long a woken thread waited for a CPU.
func SchbenchWakeupP99(b []byte) (float64, error) {
	var out struct {
		Int map[string]float64 `json:"int"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	v, ok := out.Int["wakeup_latency_pct99.0"]
	if !ok {
		return 0, errors.New("schbench: no wakeup_latency_pct99.0")
	}
	return v, nil
}

// FioReadP99 is the 99th percentile of the named job's read completion latency, in microseconds,
// from fio's JSON (--output-format=json).
func FioReadP99(b []byte, job string) (float64, error) {
	var out struct {
		Jobs []struct {
			Name string `json:"jobname"`
			Read struct {
				Clat struct {
					Percentile map[string]float64 `json:"percentile"`
				} `json:"clat_ns"`
			} `json:"read"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	for _, j := range out.Jobs {
		if j.Name == job {
			if v, ok := j.Read.Clat.Percentile["99.000000"]; ok {
				return v / 1000, nil
			}
		}
	}
	return 0, fmt.Errorf("fio: no read p99 for job %q", job)
}

var bogoRate = regexp.MustCompile(`(?m)^\s*-?\s*stressor:\s*vm\s*$[\s\S]*?bogo-ops-per-second-real-time:\s*([0-9.]+)`)

// StressVMRate is stress-ng's vm stressor's bogo operations a second (real time), from its YAML
// (--yaml): how much work got done while memory was short.
func StressVMRate(b []byte) (float64, error) {
	m := bogoRate.FindSubmatch(b)
	if m == nil {
		return 0, errors.New("stress-ng: no vm bogo-ops-per-second-real-time")
	}
	return strconv.ParseFloat(string(m[1]), 64)
}

// Median of the values, the middle one or the mean of the two middle ones.
func Median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
