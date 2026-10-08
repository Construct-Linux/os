package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A device whose block has no "Kernel driver in use" is listed; one with a driver, or with only
// the modules that could drive it, is judged by the driver line alone.
func TestDriverlessPCI(t *testing.T) {
	lspci := `00:02.0 VGA compatible controller [0300]: Intel Corporation Lunar Lake [8086:64a0] (rev 04)
	Subsystem: Lenovo Device [17aa:2310]
	Kernel driver in use: xe
	Kernel modules: xe
00:05.0 Multimedia controller [0480]: Intel Corporation Lunar Lake IPU [8086:645d] (rev 04)
	Subsystem: Lenovo Device [17aa:2310]
	Kernel modules: intel_ipu7
00:12.0 Serial controller [0700]: Intel Corporation Lunar Lake ISH [8086:a845] (rev 10)
`
	got := driverlessPCI(lspci)
	want := []string{
		"00:05.0 Multimedia controller [0480]: Intel Corporation Lunar Lake IPU [8086:645d] (rev 04)",
		"00:12.0 Serial controller [0700]: Intel Corporation Lunar Lake ISH [8086:a845] (rev 10)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("driverlessPCI = %q, want %q", got, want)
	}
}

// Both of the kernel's wordings, each file once.
func TestMissingFirmware(t *testing.T) {
	kernel := `kernel: iwlwifi 0000:00:14.3: Direct firmware load for iwlwifi-bz-b0-fm-c0-100.ucode failed with error -2
kernel: intel_ish_ipc 0000:00:12.0: Direct firmware load for intel/ish/ish_lnlm.bin failed with error -2
kernel: iwlwifi 0000:00:14.3: Direct firmware load for iwlwifi-bz-b0-fm-c0-100.ucode failed with error -2
kernel: xe 0000:00:02.0: firmware: failed to load xe/lnl_gsc_1.bin (-2)`
	got := missingFirmware(kernel)
	want := []string{"iwlwifi-bz-b0-fm-c0-100.ucode", "intel/ish/ish_lnlm.bin", "xe/lnl_gsc_1.bin"}
	if !slices.Equal(got, want) {
		t.Errorf("missingFirmware = %q, want %q", got, want)
	}
}

// Errors count by their unit, a user unit before the system one it runs under, the most first;
// a message journalctl wrote as bytes reads as text.
func TestErrorsByUnit(t *testing.T) {
	j := strings.Join([]string{
		`{"_SYSTEMD_UNIT":"serial-getty@ttyS0.service","MESSAGE":"could not get terminal name: -22"}`,
		`{"_SYSTEMD_UNIT":"serial-getty@ttyS0.service","MESSAGE":"failed to get terminal attributes"}`,
		`{"_SYSTEMD_UNIT":"user@1000.service","_SYSTEMD_USER_UNIT":"org.gnome.Shell@wayland.service","MESSAGE":[74,83]}`,
		`not json`,
	}, "\n")
	got := errorsByUnit([]byte(j), 15)
	want := []string{
		"   2  serial-getty@ttyS0.service: failed to get terminal attributes",
		"   1  org.gnome.Shell@wayland.service: JS",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errorsByUnit = %q, want %q", got, want)
	}
}

// A fingerprint that did not match and a wrong password are failures; a session opened is not.
func TestAuthFailures(t *testing.T) {
	j := strings.Join([]string{
		`{"_COMM":"fprintd","MESSAGE":"verify-no-match"}`,
		`{"SYSLOG_IDENTIFIER":"gdm-password]","MESSAGE":"pam_unix(gdm-password:auth): authentication failure; logname= uid=0"}`,
		`{"SYSLOG_IDENTIFIER":"gdm-password]","MESSAGE":"pam_unix(gdm-password:session): session opened for user ada"}`,
	}, "\n")
	got := authFailures([]byte(j), 20)
	if len(got) != 2 || !strings.HasPrefix(got[0], "fprintd: verify-no-match") || !strings.Contains(got[1], "authentication failure") {
		t.Errorf("authFailures = %q", got)
	}
}

// Health is the full charge against the design's, in energy or, where a battery reports none, in
// charge.
func TestBatteries(t *testing.T) {
	root := t.TempDir()
	for bat, files := range map[string]map[string]string{
		"BAT0": {"status": "Discharging", "capacity": "81", "energy_full": "45000000", "energy_full_design": "50000000", "cycle_count": "212"},
		"BAT1": {"status": "Full", "capacity": "100", "charge_full": "3000000", "charge_full_design": "4000000"},
	} {
		dir := filepath.Join(root, "sys/class/power_supply", bat)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, v := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := batteries(root)
	want := []string{
		"BAT0: Discharging, 81%, health 90% of its design, 212 cycles",
		"BAT1: Full, 100%, health 75% of its design",
	}
	if !slices.Equal(got, want) {
		t.Errorf("batteries = %q, want %q", got, want)
	}
}

// The header's kinds reach the archive, its salts and digests never.
func TestLUKSSummary(t *testing.T) {
	header := `{"keyslots":{"1":{"type":"luks2","kdf":{"salt":"c2FsdA=="}},"0":{"type":"luks2"}},
		"tokens":{"0":{"type":"systemd-tpm2","keyslots":["1"],"tpm2-blob":"YmxvYg=="},"1":{"type":"systemd-fido2","keyslots":["2"]}},
		"digests":{"0":{"digest":"ZGlnZXN0"}}}`
	b, err := luksSummary("/dev/nvme0n1p6", []byte(header))
	if err != nil {
		t.Fatal(err)
	}
	want := "/dev/nvme0n1p6\nkeyslot 0: luks2\nkeyslot 1: luks2\ntoken 0: systemd-tpm2, keyslots 1\ntoken 1: systemd-fido2, keyslots 2\n"
	if string(b) != want {
		t.Errorf("luksSummary =\n%s\nwant\n%s", b, want)
	}
}
