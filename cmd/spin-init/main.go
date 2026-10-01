// Command spin-init is the desktop's first process, before systemd: the kernel mounts the root
// itself, read-only under dm-verity, and there is no initrd to make /etc writable before systemd
// reads it. spin-init mounts /var - the partition labelled var - and lays /etc as an overlay: the
// image's /etc below, what this machine changed above, on /var. An update brings a new lower
// /etc; what a person changed stays theirs. Then it becomes systemd.
//
// With no var partition yet - a live stick's first boot, before systemd-repart makes one - the
// changes go to memory for this boot. On a writable root (the QEMU test disk) it only becomes
// systemd.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	systemd = "/usr/lib/systemd/systemd"
	// overlay holds /etc's upper and work directories on /var.
	overlay = "/var/lib/spin-desktop/etc"
	// varWait is how long the var partition may take to appear: the root's own partitions were
	// there when the kernel set the root up (dm-mod.waitfor), and var is on the same disk.
	varWait = 5 * time.Second
)

func main() {
	if err := setup(); err != nil {
		// A desktop that boots with a read-only /etc is better than one that does not boot:
		// say why, on the console, and carry on.
		fmt.Fprintf(os.Stderr, "spin-init: %v; /etc stays read-only\n", err)
	}
	err := unix.Exec(systemd, append([]string{systemd}, os.Args[1:]...), os.Environ())
	fmt.Fprintf(os.Stderr, "spin-init: exec %s: %v\n", systemd, err)
	os.Exit(1)
}

func setup() error {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return err
	}
	if st.Flags&unix.ST_RDONLY == 0 {
		return nil
	}
	if err := unix.Mount("sysfs", "/sys", "sysfs", unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, ""); err != nil && err != unix.EBUSY {
		return fmt.Errorf("mount /sys: %w", err)
	}
	upper, work := "", ""
	if dev, err := findPartition("var", varWait); err == nil {
		if err := unix.Mount(dev, "/var", "ext4", unix.MS_NOATIME, ""); err != nil {
			return fmt.Errorf("mount %s on /var: %w", dev, err)
		}
		upper, work = filepath.Join(overlay, "upper"), filepath.Join(overlay, "work")
	} else {
		// No /var yet: /run's tmpfs, mounted here as an initrd would - systemd keeps a /run it
		// finds mounted.
		if err := unix.Mount("tmpfs", "/run", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("mount /run for a volatile /etc: %w", err)
		}
		upper, work = "/run/spin-desktop/etc/upper", "/run/spin-desktop/etc/work"
	}
	for _, d := range []string{upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	opts := fmt.Sprintf("lowerdir=/etc,upperdir=%s,workdir=%s", upper, work)
	if err := unix.Mount("overlay", "/etc", "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay on /etc: %w", err)
	}
	return nil
}

// findPartition is the device of the partition whose GPT name is name, as the kernel lists it in
// /sys/class/block, waiting up to d for it to appear.
func findPartition(name string, d time.Duration) (string, error) {
	deadline := time.Now().Add(d)
	for {
		entries, _ := os.ReadDir("/sys/class/block")
		for _, e := range entries {
			if partName(filepath.Join("/sys/class/block", e.Name(), "uevent")) == name {
				return "/dev/" + e.Name(), nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no partition named %q", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func partName(uevent string) string {
	f, err := os.Open(uevent)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PARTNAME="); ok {
			return v
		}
	}
	return ""
}
