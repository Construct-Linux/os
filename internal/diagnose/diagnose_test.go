package diagnose

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
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
	got := DriverlessPCI(lspci)
	want := []string{
		"00:05.0 Multimedia controller [0480]: Intel Corporation Lunar Lake IPU [8086:645d] (rev 04)",
		"00:12.0 Serial controller [0700]: Intel Corporation Lunar Lake ISH [8086:a845] (rev 10)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("DriverlessPCI = %q, want %q", got, want)
	}
}

// Both of the kernel's wordings, each file once.
func TestMissingFirmware(t *testing.T) {
	kernel := `kernel: iwlwifi 0000:00:14.3: Direct firmware load for iwlwifi-bz-b0-fm-c0-100.ucode failed with error -2
kernel: intel_ish_ipc 0000:00:12.0: Direct firmware load for intel/ish/ish_lnlm.bin failed with error -2
kernel: iwlwifi 0000:00:14.3: Direct firmware load for iwlwifi-bz-b0-fm-c0-100.ucode failed with error -2
kernel: xe 0000:00:02.0: firmware: failed to load xe/lnl_gsc_1.bin (-2)`
	got := MissingFirmware(kernel)
	want := []string{"iwlwifi-bz-b0-fm-c0-100.ucode", "intel/ish/ish_lnlm.bin", "xe/lnl_gsc_1.bin"}
	if !slices.Equal(got, want) {
		t.Errorf("MissingFirmware = %q, want %q", got, want)
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
	got := ErrorsByUnit([]byte(j), 15)
	want := []string{
		"   2  serial-getty@ttyS0.service: failed to get terminal attributes",
		"   1  org.gnome.Shell@wayland.service: JS",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ErrorsByUnit = %q, want %q", got, want)
	}
}

// A fingerprint that did not match and a wrong password are failures; a session opened, or a D-Bus
// service a slow boot did not start in time, is not.
func TestAuthFailures(t *testing.T) {
	j := strings.Join([]string{
		`{"_COMM":"fprintd","MESSAGE":"verify-no-match"}`,
		`{"SYSLOG_IDENTIFIER":"gdm-password]","MESSAGE":"pam_unix(gdm-password:auth): authentication failure; logname= uid=0"}`,
		`{"SYSLOG_IDENTIFIER":"gdm-password]","MESSAGE":"pam_unix(gdm-password:session): session opened for user ada"}`,
		`{"SYSLOG_IDENTIFIER":"dbus-daemon","MESSAGE":"[system] Failed to activate service 'org.freedesktop.UPower': timed out (service_start_timeout=25000ms)"}`,
	}, "\n")
	got := AuthFailures([]byte(j), 20)
	if len(got) != 2 || !strings.HasPrefix(got[0], "fprintd: verify-no-match") || !strings.Contains(got[1], "authentication failure") {
		t.Errorf("AuthFailures = %q", got)
	}
}

// A cut counts characters, not bytes: a message in Spanish stays text.
func TestCut(t *testing.T) {
	if got := cut("autenticación  fallida\npara ada", 13); got != "autenticación…" {
		t.Errorf("cut = %q", got)
	}
	if got := cut("corto", 13); got != "corto" {
		t.Errorf("cut = %q", got)
	}
}

// A probe that fails keeps what it wrote and says why; the probes after it still run.
func TestGather(t *testing.T) {
	files := Gather(context.Background(), []Probe{
		{File: "a.txt", Run: func(context.Context) ([]byte, error) { return []byte("half"), errors.New("no sensor") }},
		{File: "b.txt", Run: func(context.Context) ([]byte, error) { return []byte("whole"), nil }},
		Command("c.txt", "/nonexistent/program"),
	})
	if got := string(files["a.txt"]); !strings.HasPrefix(got, "half") || !strings.Contains(got, "no sensor") {
		t.Errorf("a.txt = %q", got)
	}
	if got := string(files["b.txt"]); got != "whole" {
		t.Errorf("b.txt = %q", got)
	}
	if got := string(files["c.txt"]); !strings.Contains(got, "[construct diagnose:") {
		t.Errorf("c.txt = %q", got)
	}
}

// The archive holds every file under its directory, owner-readable alone; one that exists is never
// overwritten.
func TestWriteArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.tar.gz")
	if err := WriteArchive(path, "d", map[string][]byte{"summary.txt": []byte("s"), "boot/time.txt": []byte("t")}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive: %v %v", st, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if want := []string{"d/boot/time.txt", "d/summary.txt"}; !slices.Equal(names, want) {
		t.Errorf("archive holds %q, want %q", names, want)
	}
	if err := WriteArchive(path, "d", nil); err == nil {
		t.Error("an existing archive was overwritten")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the existing archive is gone: %v", err)
	}
}
