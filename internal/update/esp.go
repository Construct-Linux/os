package update

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// linuxDir is where the ESP holds the kernel images systemd-boot finds by itself.
const linuxDir = "EFI/Linux"

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
	if !slices.Contains(strings.Fields(cmdline), "roothash="+rootHash(root, verity)) {
		errs = append(errs, fmt.Errorf("its command line's roothash= is not the one partitions %s and %s halve", root, verity))
	}
	if len(errs) > 0 {
		return fmt.Errorf("update: the kernel image: %w", errors.Join(errs...))
	}
	return nil
}
