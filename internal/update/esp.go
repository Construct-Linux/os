package update

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Tries is how many times systemd-boot starts a new kernel image before it takes the boot for bad
// and falls back to the one before it: the "+3" in its name, counted down at each start, and
// taken off by systemd-bless-boot once a boot of it reached boot-complete.target.
const Tries = 3

// Linux is where the ESP holds the kernel images systemd-boot finds by itself.
const Linux = "EFI/Linux"

// Image is a kernel image on the ESP: its file, its version, and systemd-boot's count of its
// tries - left and done - when it is being counted, which a blessed image is not.
type Image struct {
	Name, Version string
	Counted       bool
	Left, Done    int
}

// Bad is an image whose tries ran out: systemd-boot boots it no more while another is there.
func (i Image) Bad() bool { return i.Counted && i.Left == 0 }

// imageName is a kernel image's name as systemd-boot counts it: <ImageID>_<version>.efi, blessed,
// or with +<left> or +<left>-<done> before the .efi.
var imageName = regexp.MustCompile(`^` + ImageID + `_` + versionPattern + `(?:\+([0-9]+)(?:-([0-9]+))?)?\.efi$`)

// ParseImage is the image named name, and false for a name that is none of bare metal's.
func ParseImage(name string) (Image, bool) {
	m := imageName.FindStringSubmatch(name)
	if m == nil {
		return Image{}, false
	}
	i := Image{Name: name, Version: m[1], Counted: m[2] != ""}
	i.Left, _ = strconv.Atoi(m[2])
	i.Done, _ = strconv.Atoi(m[3])
	return i, true
}

// Images are the kernel images in the ESP at esp.
func Images(esp string) ([]Image, error) {
	entries, err := os.ReadDir(filepath.Join(esp, Linux))
	if err != nil {
		return nil, err
	}
	var images []Image
	for _, e := range entries {
		if i, ok := ParseImage(e.Name()); ok && e.Type().IsRegular() {
			images = append(images, i)
		}
	}
	return images, nil
}

// Pending is the version of slots newer than booted that systemd-boot would boot next - one
// whose kernel image is there with tries left, or blessed - or "" for none: what a reboot moves
// the machine to.
func Pending(slots []Slot, images []Image, booted string) string {
	pending := ""
	for _, s := range slots {
		if s.Version == "" || Compare(s.Version, booted) <= 0 || (pending != "" && Compare(s.Version, pending) <= 0) {
			continue
		}
		if slices.ContainsFunc(images, func(i Image) bool { return i.Version == s.Version && !i.Bad() }) {
			pending = s.Version
		}
	}
	return pending
}

// checkImage is nil when the kernel image at path is version's and boots the slot whose
// partitions are root and verity: its os-release says the version, and its command line names both
// partitions. The release's signature vouches for its bytes; this is that they are what the
// machine is about to boot, and not another slot's.
func checkImage(path, version, root, verity string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("update: the kernel image is not a PE image: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only
	section := func(name string) (string, error) {
		s := f.Section(name)
		if s == nil {
			return "", fmt.Errorf("update: the kernel image has no %s", name)
		}
		// A section's data is padded to the file's alignment with zeros.
		raw, err := io.ReadAll(io.LimitReader(s.Open(), 1<<20)) // mutate-exempt: any bound past an os-release's and a command line's size reads them whole
		return string(bytes.TrimRight(raw, "\x00")), err
	}
	osrel, err := section(".osrel")
	if err != nil {
		return err
	}
	cmdline, err := section(".cmdline")
	if err != nil {
		return err
	}
	var errs []error
	if !slices.Contains(strings.Split(osrel, "\n"), "IMAGE_VERSION="+version) {
		errs = append(errs, fmt.Errorf("its os-release is not %s's", version))
	}
	for _, uuid := range []string{root, verity} {
		if !strings.Contains(cmdline, "PARTUUID="+uuid+" ") && !strings.Contains(cmdline, "PARTUUID="+uuid+",") {
			errs = append(errs, fmt.Errorf("its command line does not name PARTUUID=%s", uuid))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("update: the kernel image: %w", errors.Join(errs...))
	}
	return nil
}
