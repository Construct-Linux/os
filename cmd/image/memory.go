package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// What a run needs is measured here: the guest samples /proc/meminfo every second from the boot
// on, and the report has its peak - in use (MemTotal - MemAvailable) and in swap (zram) - as data,
// not a gate, as boot time is. The bench asks for more than there is on purpose, so the sampling
// stops before it.

// vmMemory is what the harness's machines are given: test:usb's peak was 2.61 GiB in use and 0.02
// GiB in swap (2026-10-04, every view and app), so 4 GB leaves the page cache over a gigabyte.
const vmMemory = "4G"

// memWatch is where the guest keeps its samples: "uptime used_kib swap_kib total_kib" a line.
const memWatch = "/run/construct-memwatch"

// startMemWatch starts the guest's sampling, a transient unit of its own.
func (g *guest) startMemWatch() {
	_, _ = g.run(`systemd-run --quiet --unit=construct-memwatch sh -c 'while :; do `+
		`u=$(cut -d" " -f1 /proc/uptime); awk -v u=$u '"'"'/^MemTotal:/ {t=$2} /^MemAvailable:/ {a=$2} /^SwapTotal:/ {st=$2} /^SwapFree:/ {sf=$2} `+
		`END {print u, t-a, st-sf, t}'"'"' /proc/meminfo >> `+memWatch+`; sleep 1; done'`, "")
}

// memSample is one of the guest's samples.
type memSample struct {
	at                float64
	used, swap, total int
}

// memPeak is the sample with the most memory in use, swap counted: what the run needed at most.
func memPeak(samples string) (memSample, int) {
	var peak memSample
	n := 0
	for _, line := range strings.Split(samples, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		at, err1 := strconv.ParseFloat(f[0], 64)
		used, err2 := strconv.Atoi(f[1])
		swap, err3 := strconv.Atoi(f[2])
		total, err4 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		n++
		if used+swap > peak.used+peak.swap {
			peak = memSample{at, used, swap, total}
		}
	}
	return peak, n
}

// memory stops the sampling and reports its peak; memory.txt keeps every sample.
func (r *report) memory(g *guest) {
	_, _ = g.run(`systemctl stop construct-memwatch`, "")
	samples, err := g.run(`cat `+memWatch, "")
	if err != nil {
		r.skip("memory", "no samples: "+err.Error())
		return
	}
	_ = os.WriteFile(filepath.Join(r.dir, "memory.txt"), []byte(samples), 0o644)
	peak, n := memPeak(samples)
	if n == 0 {
		r.skip("memory", "no samples in "+memWatch)
		return
	}
	gib := func(kib int) float64 { return float64(kib) / (1 << 20) }
	r.add("memory", true, fmt.Sprintf("peak %.2f GiB in use + %.2f GiB in swap, of %.2f GiB, at %.0fs (%d samples; memory.txt)",
		gib(peak.used), gib(peak.swap), gib(peak.total), peak.at, n))
}
