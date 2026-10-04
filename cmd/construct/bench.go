package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	disks, err := benchDisks()
	if err != nil {
		return err
	}
	kernel, dropped := bench.Writable("/", bench.Kernel(disks))
	for _, d := range dropped {
		fmt.Fprintf(stdout, "not compared: /%s, which this machine's kernel has not got\n", d)
	}
	construct, err := bench.Current("/", kernel, "construct", zramOn())
	if err != nil {
		return err
	}
	// Whatever happens, the machine's own settings come back.
	defer func() { _ = useProfile(construct) }()
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
			if err := useProfile(p); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "round %d, %s settings...\n", r+1, p.Name)
			m, err := runWorkloads(ctx, *dir, *seconds)
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

// runWorkloads runs the three workloads under the settings in place.
func runWorkloads(ctx context.Context, dir string, seconds int) (map[string]float64, error) {
	m := map[string]float64{}
	// Every CPU busy, then how long a woken thread waits for one.
	load := exec.CommandContext(ctx, "stress-ng", "--cpu", "0", "--timeout", strconv.Itoa(seconds+5)+"s", "--quiet")
	if err := load.Start(); err != nil {
		return nil, fmt.Errorf("stress-ng: %w", err)
	}
	out := filepath.Join(dir, "schbench.json")
	threads := strconv.Itoa(max(1, runtime.NumCPU()/2))
	err := exec.CommandContext(ctx, "schbench", "-m", "2", "-t", threads, "-r", strconv.Itoa(seconds), "-j", out).Run()
	_ = load.Wait()
	if err != nil {
		return nil, fmt.Errorf("schbench: %w", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if m["wakeup_p99_us"], err = bench.SchbenchWakeupP99(b); err != nil {
		return nil, err
	}

	// A large buffered write, and small direct reads beside it: what writeback limits and the I/O
	// scheduler decide is how long those reads wait.
	_ = exec.Command("sync").Run()
	fio, err := exec.CommandContext(ctx, "fio", "--output-format=json", "--directory="+dir, "--runtime="+strconv.Itoa(seconds),
		"--time_based", "--name=writer", "--rw=write", "--bs=1M", "--size=2G",
		"--name=reader", "--rw=randread", "--bs=4k", "--direct=1", "--size=256M").Output()
	if err != nil {
		return nil, fmt.Errorf("fio: %w", err)
	}
	if m["read_p99_us"], err = bench.FioReadP99(fio, "reader"); err != nil {
		return nil, err
	}

	// More memory asked for than there is: work done, and what the kernel killed for it.
	total, err := memTotalBytes()
	if err != nil {
		return nil, err
	}
	oomBefore, _ := vmstat("oom_kill")
	yaml := filepath.Join(dir, "stress.yaml")
	// In a scope of its own that outlives a kill: systemd stops a unit the kernel's OOM killer
	// reached, and stress-ng's in the caller's took the terminal, or the SSH session, with it.
	if err := exec.CommandContext(ctx, "systemd-run", "--scope", "--quiet", "-p", "OOMPolicy=continue", "--",
		"stress-ng", "--vm", "2", "--vm-bytes", strconv.FormatUint(total*6/10, 10), "--vm-keep",
		"--timeout", strconv.Itoa(seconds)+"s", "--metrics-brief", "--yaml", yaml, "--quiet").Run(); err != nil {
		return nil, fmt.Errorf("stress-ng --vm: %w", err)
	}
	oomAfter, _ := vmstat("oom_kill")
	m["oom_kills"] = float64(oomAfter - oomBefore)
	b, err = os.ReadFile(yaml)
	if err != nil {
		return nil, err
	}
	if m["vm_ops"], err = bench.StressVMRate(b); err != nil {
		return nil, err
	}
	return m, nil
}

// useProfile writes a profile's settings and turns zram swap on or off with it, then empties the
// page cache so each run starts from the same place.
func useProfile(p bench.Profile) error {
	if err := bench.Apply("/", p); err != nil {
		return err
	}
	if _, err := os.Stat("/dev/zram0"); err == nil && p.Zram != zramOn() {
		// zram-generator's units own the device: stopping its setup turns the swap off and resets
		// the device, which a second setup needs; starting the swap sets it up again.
		args := []string{"stop", "systemd-zram-setup@zram0.service"}
		if p.Zram {
			args = []string{"start", "dev-zram0.swap"}
		}
		// The reset a stop asks for finishes after systemctl returns: until the device says it holds
		// nothing, the setup a start runs finds it busy. So the stop waits for that, and a start
		// that still fails is tried again before it counts.
		var err error
		for try := 0; try < 5; try++ {
			var out []byte
			if out, err = exec.Command("systemctl", args...).CombinedOutput(); err == nil {
				break
			}
			err = fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			time.Sleep(time.Second)
		}
		if err != nil {
			log, _ := exec.Command("journalctl", "-q", "-n", "5", "-o", "cat", "-u", "systemd-zram-setup@zram0.service").Output()
			return fmt.Errorf("%w; systemd-zram-setup: %s", err, strings.TrimSpace(string(log)))
		}
		if !p.Zram {
			for i := 0; i < 50 && strings.TrimSpace(readSys("/sys/block/zram0/disksize")) != "0"; i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	_ = exec.Command("sync").Run()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o644)
}

func zramOn() bool {
	b, _ := os.ReadFile("/proc/swaps")
	return strings.Contains(string(b), "/dev/zram0")
}

// benchDisks are the machine's disks whose I/O scheduler the profiles set: not loops, zram,
// device-mapper or optical drives.
func benchDisks() ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	var disks []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "zram") || strings.HasPrefix(n, "dm-") || strings.HasPrefix(n, "sr") {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/block", n, "queue/scheduler")); err == nil {
			disks = append(disks, n)
		}
	}
	return disks, nil
}

func memTotalBytes() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			return kb * 1024, err
		}
	}
	return 0, errors.New("/proc/meminfo has no MemTotal")
}

func vmstat(key string) (uint64, error) {
	b, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == key {
			return strconv.ParseUint(v, 10, 64)
		}
	}
	return 0, fmt.Errorf("/proc/vmstat has no %s", key)
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

// readSys is a sysfs file's contents, or "" when it cannot be read.
func readSys(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
