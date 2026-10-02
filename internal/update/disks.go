package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// BlockDisk is one of the machine's disks, as the kernel describes it under /sys/block.
type BlockDisk struct {
	// Name is the kernel's: nvme0n1, sda.
	Name  string
	Size  int64
	Model string
	// Serial is what the disk says it is, which /dev/disk/by-id names it by too.
	Serial string
	// Removable and USB say a disk is a stick or a card - what the installer never picks by itself.
	Removable, USB bool
}

// Path is where d is under /dev.
func (d BlockDisk) Path() string { return "/dev/" + d.Name }

// String is d as the installer shows it to a person.
func (d BlockDisk) String() string {
	s := fmt.Sprintf("%-12s %9s", d.Path(), humanSize(d.Size))
	if d.Model != "" {
		s += "  " + d.Model
	}
	if d.Serial != "" {
		s += "  (" + d.Serial + ")"
	}
	if d.USB {
		s += "  [USB]"
	}
	return s
}

// notDisks are the kernel's block devices that are no disk a machine could be installed on: loop
// devices, RAM disks, device-mapper's, CD drives, zram, network block devices, MD arrays.
var notDisks = []string{"loop", "ram", "dm-", "sr", "zram", "nbd", "md"}

// Disks are the disks under sys (/sys) the desktop could be installed on, by name, but for the one
// it runs from, medium.
func Disks(sys, medium string) ([]BlockDisk, error) {
	entries, err := os.ReadDir(filepath.Join(sys, "block"))
	if err != nil {
		return nil, err
	}
	var disks []BlockDisk
	for _, e := range entries {
		name := e.Name()
		if name == medium || slices.ContainsFunc(notDisks, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		d, err := readDisk(sys, name)
		if err != nil {
			return nil, err
		}
		// An empty card reader, a drive with no medium in it.
		if d.Size == 0 {
			continue
		}
		disks = append(disks, d)
	}
	return disks, nil
}

// readDisk is the disk called name under sys.
func readDisk(sys, name string) (BlockDisk, error) {
	dir := filepath.Join(sys, "block", name)
	sectors, err := readTrimmed(filepath.Join(dir, "size"))
	if err != nil {
		return BlockDisk{}, err
	}
	// The kernel counts a disk's size in 512-byte sectors, whatever its own sector is.
	n, err := strconv.ParseInt(sectors, 10, 64)
	if err != nil {
		return BlockDisk{}, fmt.Errorf("install: %s's size: %w", name, err)
	}
	d := BlockDisk{Name: name, Size: n * 512}
	d.Model, _ = readTrimmed(filepath.Join(dir, "device", "model"))
	d.Serial, _ = readTrimmed(filepath.Join(dir, "device", "serial"))
	removable, _ := readTrimmed(filepath.Join(dir, "removable"))
	d.Removable = removable == "1"
	// A USB disk's device is under a USB controller's: its resolved path names usb.
	if link, err := filepath.EvalSymlinks(dir); err == nil {
		d.USB = strings.Contains(link, "/usb")
	}
	return d, nil
}

// FromStick says whether the running root is read from a removable or USB disk - a live stick -
// rather than from a disk of the machine's own.
func FromStick(sys string) bool {
	medium, err := MediumDisk(sys)
	if err != nil {
		return false
	}
	d, err := readDisk(sys, medium)
	return err == nil && (d.Removable || d.USB)
}

// Default is the disk the installer picks when it is not told: the first of disks that is neither
// removable nor on USB - the machine's own, never a stick plugged into it.
func Default(disks []BlockDisk) (BlockDisk, error) {
	for _, d := range disks {
		if !d.Removable && !d.USB {
			return d, nil
		}
	}
	return BlockDisk{}, errors.New("install: the machine has no disk of its own to install on (only removable or USB ones): name one with --disk")
}

// Resolve is the disk p names - /dev/nvme0n1, or a link to it under /dev/disk - among disks, whose
// devices are in dev (/dev). A partition, or a disk the installer did not list, is refused.
func Resolve(p, dev string, disks []BlockDisk) (BlockDisk, error) {
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return BlockDisk{}, fmt.Errorf("install: %s: %w", p, err)
	}
	for _, d := range disks {
		if filepath.Join(dev, d.Name) == target {
			return d, nil
		}
	}
	return BlockDisk{}, fmt.Errorf("install: %s is %s, which is none of the disks it could be installed on", p, target)
}

// MediumDisk is the disk the running root is read from: the device-mapper device called root, and
// what is under it - its slaves, their slaves - down to disks. A live stick's root is two of its
// partitions. The installer never lists it.
func MediumDisk(sys string) (string, error) {
	root, err := dmNamed(sys, "root")
	if err != nil {
		return "", err
	}
	disks := map[string]bool{}
	var walk func(dev string, depth int) error
	walk = func(dev string, depth int) error {
		if depth > 8 { // mutate-exempt: any bound past a real stack of devices is one
			return fmt.Errorf("install: %s is under too many devices to be a medium's", dev)
		}
		slaves, err := os.ReadDir(filepath.Join(sys, "class", "block", dev, "slaves"))
		switch {
		case err == nil && len(slaves) > 0:
			for _, s := range slaves {
				if err := walk(s.Name(), depth+1); err != nil {
					return err
				}
			}
			return nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}
		disk, err := wholeDisk(sys, dev)
		disks[disk] = true
		return err
	}
	if err := walk(root, 0); err != nil {
		return "", err
	}
	if len(disks) != 1 {
		return "", fmt.Errorf("install: the root is on %d disks, not a medium's one", len(disks))
	}
	for d := range disks {
		return d, nil
	}
	return "", nil
}

// dmNamed is the kernel's name, dm-N, of the device-mapper device called name.
func dmNamed(sys, name string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(sys, "block", "dm-*", "dm", "name"))
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		if got, err := readTrimmed(m); err == nil && got == name {
			return filepath.Base(filepath.Dir(filepath.Dir(m))), nil
		}
	}
	return "", fmt.Errorf("install: no device-mapper device called %s: this is not a Spindrift's running system", name)
}

// wholeDisk is the disk dev is: itself, or the disk it is a partition of - the directory above its
// own under /sys/class/block's link.
func wholeDisk(sys, dev string) (string, error) {
	if _, err := os.Stat(filepath.Join(sys, "class", "block", dev, "partition")); errors.Is(err, fs.ErrNotExist) {
		return dev, nil
	}
	link, err := filepath.EvalSymlinks(filepath.Join(sys, "class", "block", dev))
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Dir(link)), nil
}

// PartitionDevice is the device of partition number n of disk, by the kernel's name for it.
func PartitionDevice(sys, disk string, n int) (string, error) {
	matches, err := filepath.Glob(filepath.Join(sys, "block", disk, disk+"*", "partition"))
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		if got, err := readTrimmed(m); err == nil && got == strconv.Itoa(n) {
			return "/dev/" + filepath.Base(filepath.Dir(m)), nil
		}
	}
	return "", fmt.Errorf("install: %s has no partition %d", disk, n)
}

func readTrimmed(p string) (string, error) {
	raw, err := os.ReadFile(p)
	return strings.TrimSpace(string(raw)), err
}

// humanSize is n bytes as a person reads a disk's size.
func humanSize(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
