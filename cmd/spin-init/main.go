// Command spin-init is the desktop's first process, before systemd: the kernel mounts the root
// itself, read-only under dm-verity, and there is no initrd to make /etc writable before systemd
// reads it. spin-init mounts /var - the partition labelled var - and lays /etc as an overlay: the
// image's /etc below, what this machine changed above, on /var. An update brings a new lower
// /etc; what a person changed stays theirs. Then it becomes systemd.
//
// /var is looked for only on the disk the root was set up from - the slave of /dev/dm-0 - never
// by its label across every disk: a live stick and an installed disk both have one called var.
// With none there yet - a live stick's first boot - spin-init makes it with systemd-repart, on that
// disk alone. If that fails /var, and with it the changes to /etc, are in memory for this boot. On a
// writable root (the QEMU test disk) it mounts nothing. Either way, a root nobody is installed for
// gets the live user (internal/account) before systemd reads who there is.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/spindrift/internal/account"
	"github.com/spin-stack/spindrift/internal/gpt"
)

const (
	systemd = "/usr/lib/systemd/systemd"
	// overlay holds /etc's upper and work directories on /var.
	overlay = "/var/lib/spindrift/etc"
	// varWait is how long the var partition may take to appear: the root's own partitions were
	// there when the kernel set the root up (dm-mod.waitfor), and var is on the same disk.
	varWait = 5 * time.Second
)

func main() {
	if err := setup(); err != nil {
		// A desktop that boots with a read-only /etc is better than one that does not boot:
		// say why and carry on.
		logf(2, "%v; /etc stays read-only", err)
	}
	err := unix.Exec(systemd, append([]string{systemd}, os.Args[1:]...), os.Environ())
	logf(2, "exec %s: %v", systemd, err)
	os.Exit(1)
}

// logf writes to the kernel's log, at level (syslog's: 2 is critical, which even a quiet boot prints), which the journal keeps
// and every console the kernel prints to shows; the console spin-init has is only the last one.
func logf(level int, format string, args ...any) {
	// One line, and short: /dev/kmsg refuses a record over about a kilobyte, silently to us.
	msg := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
	if len(msg) > 900 {
		msg = "..." + msg[len(msg)-900:]
	}
	if k, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
		fmt.Fprintf(k, "<%d>spin-init: %s\n", level, msg)
		k.Close()
		return
	}
	fmt.Fprintf(os.Stderr, "spin-init: %s\n", msg)
}

func setup() error {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return err
	}
	if st.Flags&unix.ST_RDONLY == 0 {
		// The QEMU test disk: /var and /etc are the root's own, and nobody is installed on it.
		return livePerson()
	}
	if err := unix.Mount("sysfs", "/sys", "sysfs", unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, ""); err != nil && err != unix.EBUSY {
		return fmt.Errorf("mount /sys: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, ""); err != nil && err != unix.EBUSY {
		return fmt.Errorf("mount /proc: %w", err)
	}
	disk, err := rootDisk()
	if err != nil {
		return err
	}
	dev, err := findPartition(disk, "var", varWait)
	if err != nil {
		// A stick holds an image smaller than itself: its table's backup sits where the image
		// ended, and to systemd-repart the disk is full until it is moved to the end.
		if gerr := growTable("/dev/" + disk); gerr != nil {
			logf(2, "the table of %s: %v", disk, gerr)
		}
		// systemd-repart makes /var from the image's repart.d (60-var.conf) in the free space of
		// the root's disk, and formats it.
		// --seed: the new partition's UUID is made from the machine id by default, which systemd
		// has not set up yet; nothing looks /var up by it. And a /run for it to mount what it
		// formats in - only while it runs: a /run systemd finds mounted already breaks its own
		// services' sockets.
		if err := unix.Mount("tmpfs", "/run", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("mount /run for systemd-repart: %w", err)
		}
		out, rerr := exec.Command("/usr/bin/systemd-repart", "--dry-run=no", "--no-pager", "--seed=random", "/dev/"+disk).CombinedOutput()
		if err := unix.Unmount("/run", unix.MNT_DETACH); err != nil {
			return fmt.Errorf("unmount /run: %w", err)
		}
		if rerr != nil {
			logf(2, "systemd-repart on %s: %v: %s", disk, rerr, strings.TrimSpace(string(out)))
		} else {
			logf(5, "systemd-repart on %s: %s", disk, strings.TrimSpace(string(out)))
		}
		dev, err = findPartition(disk, "var", varWait)
	}
	if err == nil {
		if err := unix.Mount(dev, "/var", "ext4", unix.MS_NOATIME, ""); err != nil {
			return fmt.Errorf("mount %s on /var: %w", dev, err)
		}
	} else {
		// No /var partition: one in memory, for this boot - a live session that keeps nothing,
		// but where everything that writes can.
		logf(2, "%v; /var is in memory for this boot", err)
		if err := unix.Mount("tmpfs", "/var", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("mount a tmpfs on /var: %w", err)
		}
	}
	upper, work := filepath.Join(overlay, "upper"), filepath.Join(overlay, "work")
	for _, d := range []string{upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	opts := fmt.Sprintf("lowerdir=/etc,upperdir=%s,workdir=%s", upper, work)
	if err := unix.Mount("overlay", "/etc", "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay on /etc: %w", err)
	}
	return livePerson()
}

// livePerson gives a root nobody is installed for - a live stick, the test disk - its Live user,
// logged in at boot, before systemd reads who there is. An installed machine has its person,
// written by drift install, and never gets Live.
func livePerson() error {
	installed, err := account.HasPerson("/etc")
	if err != nil || installed {
		return err
	}
	if err := account.Write("/etc", account.Live); err != nil {
		return fmt.Errorf("the live user: %w", err)
	}
	if err := account.Autologin("/etc", account.Live.Name); err != nil {
		return err
	}
	return account.MakeHome("/var", account.Live)
}

// growTable moves dev's backup table to the disk's end, if the image it holds was smaller.
func growTable(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	var size uint64
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.BLKGETSIZE64, uintptr(unsafe.Pointer(&size))); errno != 0 {
		return errno
	}
	moved, err := gpt.Grow(f, int64(size))
	if moved {
		logf(5, "moved %s's backup table to its end", dev)
	}
	if err != nil {
		return err
	}
	return f.Sync()
}

// rootDisk is the whole disk the root's dm-verity device reads from: /dev/dm-0's slave is a
// partition, and its parent in sysfs is the disk.
func rootDisk() (string, error) {
	slaves, err := os.ReadDir("/sys/block/dm-0/slaves")
	if err != nil || len(slaves) == 0 {
		return "", fmt.Errorf("the root's device has no slaves: %v", err)
	}
	link, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", slaves[0].Name()))
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Dir(link)), nil
}

// findPartition is the device of disk's partition whose GPT name is name, read from the disk's
// own table - a partition systemd-repart just added has no name in sysfs (the kernel learns it by
// BLKPG, without one) - waiting up to d for the kernel's device to appear.
func findPartition(disk, name string, d time.Duration) (string, error) {
	f, err := os.Open("/dev/" + disk)
	if err != nil {
		return "", err
	}
	t, err := gpt.Read(f)
	f.Close()
	if err != nil {
		return "", err
	}
	p, err := t.Named(name)
	if err != nil {
		return "", fmt.Errorf("no partition named %q on %s", name, disk)
	}
	// nvme0n1's partitions are nvme0n1p1...; sda's are sda1...
	dev := fmt.Sprintf("%s%d", disk, p.Number)
	if last := disk[len(disk)-1]; last >= '0' && last <= '9' {
		dev = fmt.Sprintf("%sp%d", disk, p.Number)
	}
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(filepath.Join("/sys/block", disk, dev)); err == nil {
			return "/dev/" + dev, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s's partition %s (%s) has no device", disk, name, dev)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
