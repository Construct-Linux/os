package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// What runs on the machine itself: the workloads, and a profile put in place with the zram swap
// systemd's units own. The rest of the package decides and parses; this is the part that acts.

// Result is what one run of the workloads measured.
type Result struct {
	WakeupP99 float64 `json:"wakeup_p99_us"` // a woken thread's wait for a CPU, every CPU busy
	ReadP99   float64 `json:"read_p99_us"`   // a small read's, beside a large write
	VMOps     float64 `json:"vm_ops"`        // work done with more memory asked for than there is
	OOMKills  float64 `json:"oom_kills"`     // and what the kernel killed for it
}

// Run runs the three workloads under the settings in place - every CPU busy, a large write beside
// small reads, more memory asked for than there is - and answers their numbers.
// dir is where the I/O workload writes, on the disk being measured.
func Run(ctx context.Context, dir string, seconds int) (Result, error) {
	var m Result
	// Every CPU busy, then how long a woken thread waits for one.
	load := exec.CommandContext(ctx, "stress-ng", "--cpu", "0", "--timeout", strconv.Itoa(seconds+5)+"s", "--quiet")
	if err := load.Start(); err != nil {
		return Result{}, fmt.Errorf("stress-ng: %w", err)
	}
	out := filepath.Join(dir, "schbench.json")
	threads := strconv.Itoa(max(1, runtime.NumCPU()/2))
	err := exec.CommandContext(ctx, "schbench", "-m", "2", "-t", threads, "-r", strconv.Itoa(seconds), "-j", out).Run()
	_ = load.Wait()
	if err != nil {
		return Result{}, fmt.Errorf("schbench: %w", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return Result{}, err
	}
	if m.WakeupP99, err = schbenchWakeupP99(b); err != nil {
		return Result{}, err
	}

	// A large buffered write, and small direct reads beside it: what writeback limits and the I/O
	// scheduler decide is how long those reads wait.
	_ = exec.Command("sync").Run()
	fio, err := exec.CommandContext(ctx, "fio", "--output-format=json", "--directory="+dir, "--runtime="+strconv.Itoa(seconds),
		"--time_based", "--name=writer", "--rw=write", "--bs=1M", "--size=2G",
		"--name=reader", "--rw=randread", "--bs=4k", "--direct=1", "--size=256M").Output()
	if err != nil {
		return Result{}, fmt.Errorf("fio: %w", err)
	}
	if m.ReadP99, err = fioReadP99(fio, "reader"); err != nil {
		return Result{}, err
	}

	// More memory asked for than there is: work done, and what the kernel killed for it.
	total, err := memTotalBytes()
	if err != nil {
		return Result{}, err
	}
	oomBefore, _ := vmstat("oom_kill")
	yaml := filepath.Join(dir, "stress.yaml")
	// In a scope of its own that outlives a kill: systemd stops a unit the kernel's OOM killer
	// reached, and stress-ng's in the caller's took the terminal, or the SSH session, with it.
	if err := exec.CommandContext(ctx, "systemd-run", "--scope", "--quiet", "-p", "OOMPolicy=continue", "--",
		"stress-ng", "--vm", "2", "--vm-bytes", strconv.FormatUint(total*6/10, 10), "--vm-keep",
		"--timeout", strconv.Itoa(seconds)+"s", "--metrics-brief", "--yaml", yaml, "--quiet").Run(); err != nil {
		return Result{}, fmt.Errorf("stress-ng --vm: %w", err)
	}
	oomAfter, _ := vmstat("oom_kill")
	m.OOMKills = float64(oomAfter - oomBefore)
	b, err = os.ReadFile(yaml)
	if err != nil {
		return Result{}, err
	}
	if m.VMOps, err = stressVMRate(b); err != nil {
		return Result{}, err
	}
	return m, nil
}

// Use puts the machine under p: its settings, zram swap on or off with it, then the page cache
// emptied so each run starts from the same place.
func Use(p Profile) error {
	if err := apply("/", p); err != nil {
		return err
	}
	if _, err := os.Stat("/dev/zram0"); err == nil && p.Zram != ZramOn() {
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
			if err := zramEmpty(); err != nil {
				return err
			}
		}
	}
	_ = exec.Command("sync").Run()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o644)
}

// zramEmpty waits for the stop's reset to empty the device, and resets it itself if the reset did
// not take - something held the device open the moment it was asked, and it stays set up: the next
// setup then fails on its comp_algorithm (busy), and systemd retries it into start-limit-hit, a
// failed unit and the journal's errors (test:usb, 2026-10-04).
func zramEmpty() error {
	const disksize, reset = "/sys/block/zram0/disksize", "/sys/block/zram0/reset"
	for try := 0; try < 10; try++ {
		for i := 0; i < 10; i++ {
			if strings.TrimSpace(readSys(disksize)) == "0" {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = os.WriteFile(reset, []byte("1"), 0o200)
	}
	return fmt.Errorf("zram0 kept its %s bytes after it was stopped and reset", strings.TrimSpace(readSys(disksize)))
}

// ZramOn says whether swap is zram's now.
func ZramOn() bool {
	b, _ := os.ReadFile("/proc/swaps")
	return strings.Contains(string(b), "/dev/zram0")
}

// Disks are the machine's disks whose I/O scheduler the profiles set: not loops, zram,
// device-mapper or optical drives.
func Disks() ([]string, error) {
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

// readSys is a sysfs file's contents, or "" when it cannot be read.
func readSys(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
