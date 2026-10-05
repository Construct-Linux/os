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

// benchCmd measures CONSTRUCT's memory, writeback, scheduler and THP settings against the kernel's
// own defaults on this machine (internal/bench): rounds of the same three workloads under each,
// in A B B A order, then puts the machine's settings back. Numbers from a VM are not a laptop's.
func benchCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("bench", flag.ContinueOnError)
	rounds := fl.Int("rounds", 3, "rounds of every workload under each profile")
	seconds := fl.Int("seconds", 20, "how long each workload runs")
	dir := fl.String("dir", "/var/tmp/construct-bench", "where the I/O workload writes (on /var's disk)")
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
	construct, err := bench.Current("/", kernel, "construct", bench.ZramOn())
	if err != nil {
		return err
	}
	// Whatever happens, the machine's own settings come back.
	defer func() { _ = bench.Use(construct) }()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(*dir)

	results := map[string]map[string][]float64{}
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
			m, err := bench.Run(ctx, *dir, *seconds)
			if err != nil {
				return err
			}
			if results[p.Name] == nil {
				results[p.Name] = map[string][]float64{}
			}
			for k, v := range m {
				results[p.Name][k] = append(results[p.Name][k], v)
			}
		}
	}
	fmt.Fprint(stdout, benchTable(results))
	return saveBench(results)
}

// The metrics, how each reads, and whether more is better.
var benchMetrics = []struct {
	key, label   string
	higherBetter bool
}{
	{"wakeup_p99_us", "wakeup latency p99, all CPUs busy (µs)", false},
	{"read_p99_us", "random read latency p99 beside a large write (µs)", false},
	{"vm_ops", "work done with 120% of RAM asked for (ops/s)", true},
	{"oom_kills", "processes killed for memory", false},
}

func benchTable(results map[string]map[string][]float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-54s %12s %12s %8s\n", "median of each workload", "kernel", "construct", "change")
	for _, m := range benchMetrics {
		k, c := bench.Median(results["kernel"][m.key]), bench.Median(results["construct"][m.key])
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
func saveBench(results map[string]map[string][]float64) error {
	dir := "/var/log/construct"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "bench-"+time.Now().Format("20060102-150405")+".json")
	return os.WriteFile(p, b, 0o644)
}
