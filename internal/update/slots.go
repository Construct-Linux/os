package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/spin-desktop/internal/gpt"
)

// The partition types of a slot, x86-64's in the Discoverable Partitions Specification: what the
// build's repart.d gives them (Type=root, Type=root-verity).
const (
	TypeRoot       = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
	TypeRootVerity = "2c7357ed-ebd2-46d9-aec1-23d437ec2bf5"
)

// Empty is systemd-sysupdate's label for a partition it may write: slot B's until the first
// update, and a slot's while it is being written.
const Empty = "_empty"

// Slot is a root partition and the verity partition beside it, labelled alike: Empty, or
// <ImageID>_<Version>.
type Slot struct {
	Label, Version string
	Root, Verity   gpt.Partition
}

// Slots are the slots of t, in the order of their roots: each root partition paired with the
// first verity partition of its label not paired yet. A slot's partition labelled as no slot is,
// or one with no partner, is refused: the disk is not the layout an update writes into.
func Slots(t gpt.Table) ([]Slot, error) {
	var slots []Slot
	paired := map[int]bool{}
	for _, root := range t.Partitions {
		if root.Type != TypeRoot {
			continue
		}
		version, err := labelVersion(root.Name)
		if err != nil {
			return nil, err
		}
		j := slices.IndexFunc(t.Partitions, func(p gpt.Partition) bool {
			return p.Type == TypeRootVerity && p.Name == root.Name && !paired[int(p.First)]
		})
		if j < 0 {
			return nil, fmt.Errorf("update: the root partition %s (%s) has no verity partition labelled alike", root.UUID, root.Name)
		}
		paired[int(t.Partitions[j].First)] = true
		slots = append(slots, Slot{Label: root.Name, Version: version, Root: root, Verity: t.Partitions[j]})
	}
	for _, p := range t.Partitions {
		if p.Type == TypeRootVerity && !paired[int(p.First)] {
			return nil, fmt.Errorf("update: the verity partition %s (%s) has no root partition labelled alike", p.UUID, p.Name)
		}
	}
	return slots, nil
}

// labelVersion is the version a slot's label names, "" for Empty.
func labelVersion(label string) (string, error) {
	if label == Empty {
		return "", nil
	}
	v, ok := strings.CutPrefix(label, ImageID+"_")
	if !ok || !slotVersion.MatchString(v) {
		return "", fmt.Errorf("update: a slot labelled %q, which is neither %s nor %s_<version>", label, Empty, ImageID)
	}
	return v, nil
}

var slotVersion = regexp.MustCompile(`^` + versionPattern + `$`)

// Booted is the slot of slots whose partitions start at the sectors starts: the ones under the
// running root. Its label must name version, the running root's own: a slot that holds another
// version than it says is one no update may reason about.
func Booted(slots []Slot, starts []int64, version string) (Slot, error) {
	for _, s := range slots {
		if slices.Contains(starts, s.Root.First) && slices.Contains(starts, s.Verity.First) {
			if s.Version != version {
				return Slot{}, fmt.Errorf("update: the running root, %s, is on the slot labelled %s", version, s.Label)
			}
			return s, nil
		}
	}
	return Slot{}, errors.New("update: the running root is on none of the disk's slots")
}

// Target is the slot of slots to write the next version into: never the booted one; an empty one
// first; else the one holding the oldest version.
func Target(slots []Slot, booted Slot) (Slot, error) {
	var target *Slot
	for i, s := range slots {
		switch {
		case s.Root.First == booted.Root.First:
		case s.Version == "":
			return s, nil
		case target == nil || Compare(s.Version, target.Version) < 0: // mutate-exempt: two slots of one version are the same choice
			target = &slots[i]
		}
	}
	if target == nil {
		return Slot{}, errors.New("update: the disk has no slot but the running one")
	}
	return *target, nil
}

// RootDevice is where the running root is: the disk its dm-verity device is on, and the first
// sector of each partition under it, as sysfs counts them in 512-byte sectors.
type RootDevice struct {
	Disk   string
	Starts []int64
}

// FindRoot is the device the running root is on, dev (stat("/")'s st_dev), under sys: its dm-verity
// device's two partitions, which must be on one disk, whose sectors must be 512 bytes - the size
// the disk's table is read and written in (internal/gpt).
func FindRoot(sys string, dev uint64) (RootDevice, error) {
	slaves := filepath.Join(sys, "dev", "block", fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev)), "slaves")
	entries, err := os.ReadDir(slaves)
	if err != nil {
		return RootDevice{}, fmt.Errorf("update: the root is not a device-mapper device over partitions: %w", err)
	}
	var r RootDevice
	for _, e := range entries {
		part := filepath.Join(sys, "class", "block", e.Name())
		link, err := filepath.EvalSymlinks(part)
		if err != nil {
			return RootDevice{}, err
		}
		if _, err := os.Stat(filepath.Join(link, "partition")); err != nil {
			return RootDevice{}, fmt.Errorf("update: %s, under the root, is not a partition", e.Name())
		}
		disk := filepath.Base(filepath.Dir(link))
		if r.Disk != "" && r.Disk != disk {
			return RootDevice{}, fmt.Errorf("update: the root is over two disks, %s and %s", r.Disk, disk)
		}
		r.Disk = disk
		start, err := readNumber(filepath.Join(link, "start"))
		if err != nil {
			return RootDevice{}, err
		}
		r.Starts = append(r.Starts, start)
	}
	if len(r.Starts) != 2 {
		return RootDevice{}, fmt.Errorf("update: the root is over %d partitions, not a slot's two", len(r.Starts))
	}
	size, err := readNumber(filepath.Join(sys, "class", "block", r.Disk, "queue", "logical_block_size"))
	if err != nil {
		return RootDevice{}, err
	}
	if size != gpt.Sector {
		return RootDevice{}, fmt.Errorf("update: %s has sectors of %d bytes, and its table is read in %d", r.Disk, size, gpt.Sector)
	}
	r.Disk = "/dev/" + r.Disk
	return r, nil
}

// readNumber is the number the sysfs file at p holds.
func readNumber(p string) (int64, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("update: %s: %w", p, err)
	}
	return n, nil
}
