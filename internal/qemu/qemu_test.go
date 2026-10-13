package qemu

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The panel QEMU is asked for is the one the guest's virtio GPU reports; without one, QEMU's own
// default.
func TestGPU(t *testing.T) {
	if d := (Machine{Width: 2880, Height: 1800}).gpu(); d != "virtio-vga,xres=2880,yres=1800" {
		t.Errorf("2880x1800: %q", d)
	}
	if d := (Machine{}).gpu(); d != "virtio-vga" {
		t.Errorf("no panel: %q", d)
	}
}

// fakeSwtpm puts first on PATH an swtpm that makes its control socket only when socket is set,
// then waits as swtpm does for a QEMU; it answers the file its process id is written to.
func fakeSwtpm(t *testing.T, socket bool) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\n"
	if socket {
		script += `for a; do case $a in type=unixio,path=*) : > "${a#type=unixio,path=}";; esac; done` + "\n"
	}
	script += "exec sleep 1000\n"
	if err := os.WriteFile(filepath.Join(dir, "swtpm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pidFile
}

// gone fails t when the process whose id is in pidFile still runs.
func gone(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Errorf("swtpm (%d) still runs: %v", pid, err)
	}
}

// A swtpm whose QEMU never comes is stopped, not left running until the harness exits: one that
// made no socket, and one stopped because QEMU did not start.
func TestAnSwtpmNoQEMUUsesIsStopped(t *testing.T) {
	pidFile := fakeSwtpm(t, true)
	sock, stop, err := startTPM(t.TempDir())
	if err != nil || !strings.HasSuffix(sock, "swtpm.sock") {
		t.Fatalf("startTPM = %q, %v", sock, err)
	}
	stop()
	gone(t, pidFile)

	pidFile = fakeSwtpm(t, false)
	if _, _, err := startTPM(t.TempDir()); err == nil {
		t.Fatal("an swtpm without a socket was taken")
	}
	gone(t, pidFile)
}
