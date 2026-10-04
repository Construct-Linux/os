package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// machine is a root with the files a report reads: an installed ThinkPad booted from slot A of
// nvme0n1, slot B holding the release before it, /var LUKS2, Secure Boot on, a TPM 2.0, an Intel
// GPU on xe and an NVIDIA one without a driver.
func machine(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"usr/lib/os-release":                    "ID=construct\nIMAGE_VERSION=20261004.1\n",
		"proc/sys/kernel/osrelease":             "7.2.8\n",
		"sys/class/dmi/id/sys_vendor":           "LENOVO\n",
		"sys/class/dmi/id/product_name":         "21Q6\n",
		"sys/class/dmi/id/product_version":      "ThinkPad X9-15 Gen 1\n",
		"sys/class/dmi/id/bios_version":         "N3LET12W (1.05 )\n",
		"proc/cpuinfo":                          "processor\t: 0\nmodel name\t: Intel(R) Core(TM) Ultra 7 258V\n\nprocessor\t: 1\nmodel name\t: Intel(R) Core(TM) Ultra 7 258V\n",
		"proc/meminfo":                          "MemTotal:       32554432 kB\nMemFree:  1 kB\n",
		"sys/class/tpm/tpm0/tpm_version_major":  "2\n",
		"sys/block/dm-0/dm/name":                "root\n",
		"sys/block/dm-1/dm/name":                "var\n",
		"sys/block/dm-1/dm/uuid":                "CRYPT-LUKS2-0123456789abcdef-var\n",
		"sys/block/nvme0n1/nvme0n1p1/uevent":    "PARTN=1\nPARTNAME=esp\n",
		"sys/block/nvme0n1/nvme0n1p2/uevent":    "PARTN=2\nPARTNAME=construct_20261004.1\n",
		"sys/block/nvme0n1/nvme0n1p2/partition": "2\n",
		"sys/block/nvme0n1/nvme0n1p3/uevent":    "PARTN=3\nPARTNAME=construct_20261004.1\n",
		"sys/block/nvme0n1/nvme0n1p4/uevent":    "PARTN=4\nPARTNAME=construct_20260927.2\n",
		"sys/block/nvme0n1/nvme0n1p5/uevent":    "PARTN=5\nPARTNAME=construct_20260927.2\n",
		"sys/block/nvme0n1/nvme0n1p6/uevent":    "PARTN=6\nPARTNAME=var\n",
		"sys/class/drm/card0/device/vendor":     "0x8086\n",
		"sys/class/drm/card0/device/device":     "0x64a0\n",
		"sys/class/drm/card1/device/vendor":     "0x10de\n",
		"sys/class/drm/card1/device/device":     "0x28e1\n",
		"sys/class/drm/card0-eDP-1/status":      "connected\n",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(root, name), []byte(body))
	}
	// efivarfs: four bytes of attributes, then the value.
	efi := filepath.Join(root, "sys/firmware/efi/efivars")
	writeFile(t, filepath.Join(efi, "SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"), []byte{6, 0, 0, 0, 1})
	writeFile(t, filepath.Join(efi, "SetupMode-8be4df61-93ca-11d2-aa0d-00e098032b8c"), []byte{6, 0, 0, 0, 0})
	// The root is dm-0 over nvme0n1p2: sysfs's class/block links into block/.
	link(t, "../../block/nvme0n1/nvme0n1p2", filepath.Join(root, "sys/class/block/nvme0n1p2"))
	writeFile(t, filepath.Join(root, "sys/class/block/dm-0/slaves/nvme0n1p2"), nil)
	link(t, "../../../../bus/pci/drivers/xe", filepath.Join(root, "sys/class/drm/card0/device/driver"))
	return root
}

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// commands answers systemctl and journalctl as a boot with one failed unit and twelve errors.
func commands(_ context.Context, name string, _ ...string) ([]byte, error) {
	switch name {
	case "systemctl":
		return []byte("fwupd-refresh.service loaded failed failed Refresh fwupd metadata\n"), nil
	case "journalctl":
		var b strings.Builder
		for i := 1; i <= 12; i++ {
			fmt.Fprintf(&b, "Oct 04 10:00:%02d construct kernel: error %d\n", i, i)
		}
		return []byte(b.String()), nil
	}
	return nil, errors.New("not here")
}

func report(t *testing.T, root string, run runner) string {
	t.Helper()
	facts, journal := gatherReport(t.Context(), root, run)
	var b strings.Builder
	writeReport(&b, facts, journal)
	return b.String()
}

func TestReport(t *testing.T) {
	got := report(t, machine(t), commands)
	for _, want := range []string{
		"version:         20261004.1\n",
		"other slot:      20260927.2\n",
		"kernel:          7.2.8\n",
		"vendor:          LENOVO\n",
		"product:         21Q6\n",
		"product version: ThinkPad X9-15 Gen 1\n",
		"bios:            N3LET12W (1.05 )\n",
		"cpu:             Intel(R) Core(TM) Ultra 7 258V\n",
		"memory:          31.0 GiB\n",
		"secure boot:     enabled\n",
		"tpm 2.0:         yes\n",
		"/var encrypted:  yes (LUKS2)\n",
		"gpu:             8086:64a0 (xe), 10de:28e1 (no driver)\n",
		"failed units:    fwupd-refresh.service\n",
		"journal errors:  12 this boot\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	// The last ten lines, not the first two.
	if strings.Contains(got, "error 2\n") || !strings.Contains(got, "  Oct 04 10:00:03 construct kernel: error 3\n") || !strings.Contains(got, "error 12\n") {
		t.Errorf("not the last ten journal lines:\n%s", got)
	}
}

// Before the first update slot B is empty; a firmware in setup mode enforces nothing; a live
// stick's /var is no LUKS device.
func TestReportStates(t *testing.T) {
	root := machine(t)
	for _, p := range []string{"sys/block/nvme0n1/nvme0n1p4/uevent", "sys/block/nvme0n1/nvme0n1p5/uevent"} {
		writeFile(t, filepath.Join(root, p), []byte("PARTNAME=_empty\n"))
	}
	writeFile(t, filepath.Join(root, "sys/firmware/efi/efivars/SetupMode-8be4df61-93ca-11d2-aa0d-00e098032b8c"), []byte{6, 0, 0, 0, 1})
	writeFile(t, filepath.Join(root, "sys/block/dm-1/dm/name"), []byte("other\n"))
	got := report(t, root, commands)
	for _, want := range []string{"other slot:      empty\n", "secure boot:     disabled (setup mode)\n", "/var encrypted:  no\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
}

// A root that tells nothing and programs that fail still make a report: every fact unknown, a
// clean boot's journal and units say so.
func TestReportOfNothing(t *testing.T) {
	got := report(t, t.TempDir(), func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no such program")
	})
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasSuffix(l, " unknown") {
			t.Errorf("%q is not unknown", l)
		}
	}
	got = report(t, t.TempDir(), func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	for _, want := range []string{"failed units:    none\n", "journal errors:  0 this boot\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
}
