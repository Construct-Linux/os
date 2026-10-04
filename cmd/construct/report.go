package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Construct-Linux/os/internal/update"
)

// unknown is a fact the machine did not tell: the report goes on without it.
const unknown = "unknown"

// runner runs a program and answers its standard output.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// fact is one line of a report.
type fact struct{ name, value string }

// reportCmd prints what a bug report needs to know of this machine, as text a person pastes into
// an issue: the versions in both slots, the kernel, the machine and its firmware, what protects
// the disk, the GPUs, and what failed this boot.
func reportCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: construct report")
	}
	facts, journal := gatherReport(ctx, "/", execRunner)
	writeReport(out, facts, journal)
	return nil
}

// gatherReport reads each fact under root, the programs' through run, and answers them and the
// journal's errors.
func gatherReport(ctx context.Context, root string, run runner) ([]fact, []string) {
	booted := bootedVersion(root)
	facts := []fact{
		{"version", booted},
		{"other slot", otherSlot(root, booted)},
		{"kernel", firstLine(filepath.Join(root, "proc/sys/kernel/osrelease"))},
	}
	for _, f := range []struct{ name, file string }{
		{"vendor", "sys_vendor"}, {"product", "product_name"},
		{"product version", "product_version"}, {"bios", "bios_version"},
	} {
		facts = append(facts, fact{f.name, firstLine(filepath.Join(root, "sys/class/dmi/id", f.file))})
	}
	facts = append(facts,
		fact{"cpu", cpuModel(root)},
		fact{"memory", memTotal(root)},
		fact{"secure boot", secureBoot(root)},
		fact{"tpm 2.0", tpm(root)},
		fact{"/var encrypted", varEncrypted(root)},
		fact{"gpu", strings.Join(gpus(root), ", ")},
		fact{"failed units", failedUnits(ctx, run)},
	)
	return facts, journalErrors(ctx, run)
}

// writeReport writes facts aligned, then journal: its count, and the last lines of it.
func writeReport(w io.Writer, facts []fact, journal []string) {
	for _, f := range facts {
		v := f.value
		if v == "" {
			v = unknown
		}
		fmt.Fprintf(w, "%-16s %s\n", f.name+":", v)
	}
	if journal == nil {
		fmt.Fprintf(w, "%-16s %s\n", "journal errors:", unknown)
		return
	}
	fmt.Fprintf(w, "%-16s %d this boot\n", "journal errors:", len(journal))
	for _, l := range journal[max(0, len(journal)-10):] {
		fmt.Fprintf(w, "  %s\n", l)
	}
}

// firstLine is the first line of the file at path, trimmed, or "" when it cannot be read.
func firstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

func bootedVersion(root string) string {
	v, err := update.BootedVersion(filepath.Join(root, "usr/lib/os-release"))
	if err != nil {
		return ""
	}
	return v
}

// otherSlot is the version in the slot the machine did not boot, from the labels of the
// partitions on the root's disk: "empty" before the first update, "none" on a disk of one slot.
func otherSlot(root, booted string) string {
	sys := filepath.Join(root, "sys")
	disk, err := update.MediumDisk(sys)
	if err != nil {
		return ""
	}
	events, err := filepath.Glob(filepath.Join(sys, "block", disk, disk+"*", "uevent"))
	if err != nil || len(events) == 0 {
		return ""
	}
	var others []string
	empty := false
	for _, e := range events {
		label := ueventValue(e, "PARTNAME")
		if label == update.Empty {
			empty = true
		}
		if v, ok := strings.CutPrefix(label, update.ImageID+"_"); ok && v != booted && !slices.Contains(others, v) {
			others = append(others, v)
		}
	}
	switch {
	case len(others) > 0:
		return strings.Join(others, ", ")
	case empty:
		return "empty"
	}
	return "none"
}

// ueventValue is key's value in the uevent file at path.
func ueventValue(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// procField is the value of the first "key: value" line named key in the /proc file at path.
func procField(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cpuModel(root string) string {
	return procField(filepath.Join(root, "proc/cpuinfo"), "model name")
}

func memTotal(root string) string {
	kb, err := strconv.ParseInt(strings.TrimSuffix(procField(filepath.Join(root, "proc/meminfo"), "MemTotal"), " kB"), 10, 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%.1f GiB", float64(kb)/(1<<20))
}

// secureBoot is the firmware's SecureBoot variable; in setup mode it enforces nothing whatever
// that says.
func secureBoot(root string) string {
	vars := update.Vars(filepath.Join(root, update.EFIVars))
	sb, err := vars.Read("SecureBoot", update.Global)
	if err != nil || len(sb) != 1 {
		return ""
	}
	if setup, err := vars.Read("SetupMode", update.Global); err == nil && len(setup) == 1 && setup[0] == 1 {
		return "disabled (setup mode)"
	}
	if sb[0] == 1 {
		return "enabled"
	}
	return "disabled"
}

func tpm(root string) string {
	if hasTPM2(filepath.Join(root, "sys")) {
		return "yes"
	}
	if _, err := os.Stat(filepath.Join(root, "sys/class/tpm/tpm0")); err == nil {
		return "no (not a TPM 2.0)"
	}
	if _, err := os.Stat(filepath.Join(root, "sys/class")); err != nil {
		return ""
	}
	return "no"
}

// varEncrypted says whether the device-mapper device called var - systemd-cryptsetup's, opened
// in the initrd - is LUKS: a live stick's /var is a plain partition.
func varEncrypted(root string) string {
	names, err := filepath.Glob(filepath.Join(root, "sys/block/dm-*/dm/name"))
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(root, "sys/block")); err != nil {
		return ""
	}
	for _, n := range names {
		if firstLine(n) != "var" {
			continue
		}
		uuid := firstLine(filepath.Join(filepath.Dir(n), "uuid"))
		if kind, ok := strings.CutPrefix(uuid, "CRYPT-"); ok {
			kind, _, _ = strings.Cut(kind, "-")
			return "yes (" + kind + ")"
		}
	}
	return "no"
}

// gpus are the DRM cards' PCI ids and drivers: vendor:device (driver).
func gpus(root string) []string {
	cards, err := filepath.Glob(filepath.Join(root, "sys/class/drm/card*"))
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range cards {
		// card0-eDP-1 and its kin are a card's connectors.
		if strings.Contains(filepath.Base(c), "-") {
			continue
		}
		dev := filepath.Join(c, "device")
		vendor := strings.TrimPrefix(firstLine(filepath.Join(dev, "vendor")), "0x")
		device := strings.TrimPrefix(firstLine(filepath.Join(dev, "device")), "0x")
		if vendor == "" || device == "" {
			continue
		}
		driver := "no driver"
		if link, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			driver = filepath.Base(link)
		} else if !errors.Is(err, fs.ErrNotExist) {
			driver = unknown
		}
		out = append(out, fmt.Sprintf("%s:%s (%s)", vendor, device, driver))
	}
	return out
}

// failedUnits are the units systemd lists as failed, or "none".
func failedUnits(ctx context.Context, run runner) string {
	b, err := run(ctx, "systemctl", "--failed", "--no-legend", "--plain")
	if err != nil {
		return ""
	}
	var units []string
	for _, l := range lines(b) {
		if f := strings.Fields(l); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	if len(units) == 0 {
		return "none"
	}
	return strings.Join(units, ", ")
}

// journalErrors are the journal's lines of priority err and worse this boot: nil when it could
// not be read, empty when there are none.
func journalErrors(ctx context.Context, run runner) []string {
	b, err := run(ctx, "journalctl", "-b", "-p", "err", "-q", "--no-pager")
	if err != nil {
		return nil
	}
	return append([]string{}, lines(b)...)
}

func lines(b []byte) []string {
	var out []string
	for l := range strings.SplitSeq(string(bytes.TrimSpace(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
