package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/bench"
)

// benchDir is where the I/O workload writes: on /var's disk.
const benchDir = "/var/tmp/construct-bench"

// benchCmd measures CONSTRUCT's memory, writeback, scheduler and THP settings against the kernel's
// own defaults on this machine (internal/bench): rounds of the same three workloads under each,
// in A B B A order, then puts the machine's settings back. Numbers from a VM are not a laptop's.
func benchCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("bench", flag.ContinueOnError)
	rounds := fl.Int("rounds", 3, "rounds of every workload under each profile")
	seconds := fl.Int("seconds", 20, "how long each workload runs")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("bench changes the kernel's settings: sudo construct bench")
	}
	disks, err := bench.Disks()
	if err != nil {
		return err
	}
	kernel, dropped := bench.Writable("/", bench.Kernel(disks))
	for _, d := range dropped {
		fmt.Fprintf(stdout, "not compared: /%s, which this machine's kernel has not got\n", d)
	}
	construct, err := bench.Current("/", kernel, bench.ZramOn())
	if err != nil {
		return err
	}
	// Whatever happens, the machine's own settings come back.
	defer func() { _ = bench.Use(construct) }()
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(benchDir)

	runs := map[string][]bench.Result{}
	order := []bench.Profile{kernel, construct}
	for r := 0; r < *rounds; r++ {
		for i := range order {
			p := order[i]
			if r%2 == 1 {
				p = order[len(order)-1-i]
			}
			// A signal ends the run between rounds too, so the deferred restore runs at once.
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := bench.Use(p); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "round %d, %s settings...\n", r+1, p.Name)
			res, err := bench.Run(ctx, benchDir, *seconds)
			if err != nil {
				return err
			}
			runs[p.Name] = append(runs[p.Name], res)
		}
	}
	fmt.Fprint(stdout, benchTable(runs[kernel.Name], runs[construct.Name]))
	return saveBench(runs)
}

// The metrics, how each reads, and whether more is better.
var benchMetrics = []struct {
	label        string
	of           func(bench.Result) float64
	higherBetter bool
}{
	{"wakeup latency p99, all CPUs busy (µs)", func(r bench.Result) float64 { return r.WakeupP99 }, false},
	{"random read latency p99 beside a large write (µs)", func(r bench.Result) float64 { return r.ReadP99 }, false},
	{"work done with 120% of RAM asked for (ops/s)", func(r bench.Result) float64 { return r.VMOps }, true},
	{"processes killed for memory", func(r bench.Result) float64 { return r.OOMKills }, false},
}

// benchTable compares the median of each metric over the kernel's runs and CONSTRUCT's.
func benchTable(kernel, construct []bench.Result) string {
	median := func(runs []bench.Result, of func(bench.Result) float64) float64 {
		var v []float64
		for _, r := range runs {
			v = append(v, of(r))
		}
		return bench.Median(v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-54s %12s %12s %8s\n", "median of each workload", "kernel", "construct", "change")
	for _, m := range benchMetrics {
		k, c := median(kernel, m.of), median(construct, m.of)
		change := "-"
		if k != 0 {
			pct := 100 * (c - k) / k
			better := (pct > 0) == m.higherBetter
			sign := "worse"
			if better || pct == 0 {
				sign = "better"
			}
			change = fmt.Sprintf("%+.0f%% %s", pct, sign)
		}
		fmt.Fprintf(&b, "%-54s %12.1f %12.1f %8s\n", m.label, k, c, change)
	}
	return b.String()
}

// saveBench keeps the run's numbers in /var/log/construct, to compare a release with the next.
func saveBench(runs map[string][]bench.Result) error {
	dir := "/var/log/construct"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "bench-"+time.Now().Format("20060102-150405")+".json")
	return os.WriteFile(p, b, 0o644)
}
