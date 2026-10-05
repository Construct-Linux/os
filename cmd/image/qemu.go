package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Construct-Linux/os/internal/qemu"
)

func boot(args []string) error {
	fl := flag.NewFlagSet("boot", flag.ExitOnError)
	shot := fl.String("shot", "", "take a screenshot (PPM) here after -after, then stop the VM")
	after := fl.Duration("after", 90*time.Second, "how long after start to take the screenshot")
	mem := fl.String("m", "4G", "memory")
	stick := fl.Bool("usb", false, "boot the USB disk by UEFI from a stick, as a machine does")
	shots := fl.Int("shots", 0, "take this many screenshots, one a second from the start, into _output/boot/, then stop")
	scale := fl.Int("scale", 2, "2: the X9-15's 2880x1800 panel; 1: 1280x800")
	_ = fl.Parse(args)

	sc, err := screenFor(*scale)
	if err != nil {
		return err
	}
	m, err := testMachine(*mem, "", *stick)
	if err != nil {
		return err
	}
	m.Width, m.Height = sc.W, sc.H
	v, err := m.Start()
	if err != nil {
		return err
	}
	if *shots > 0 {
		dir := filepath.Join(out, "boot")
		_ = os.RemoveAll(dir)
		_ = os.MkdirAll(dir, 0o755)
		for i := range *shots {
			time.Sleep(time.Second)
			_ = v.PNG(filepath.Join(dir, fmt.Sprintf("%02d.png", i+1)))
		}
		_ = v.Stop()
		return nil
	}
	fmt.Println("VNC 127.0.0.1:5907, serial", filepath.Join(out, "serial.log"))
	if *shot == "" {
		return v.Wait()
	}
	time.Sleep(*after)
	err = v.Screendump(*shot)
	_ = v.Stop()
	return err
}

// testMachine is the machine that boots _output/disk.raw with the kernel given directly, or with
// stick the USB disk (_output/usb/disk.raw) as a machine boots it: UEFI firmware with variables of
// its own, the image on a USB stick of 32 GB, of which the first boot makes /var. With pubkey the
// harness reaches the guest's sshd (qemu.Machine.PubKey).
func testMachine(mem, pubkey string, stick bool) (qemu.Machine, error) {
	m := qemu.Machine{Memory: mem, PubKey: pubkey, Dir: out,
		Kernel: filepath.Join(out, "kernel", "bzImage"), Disk: filepath.Join(out, "disk.raw")}
	if stick {
		return withStick(m, filepath.Join(out, "usb"))
	}
	return m, nil
}

// withStick is m with fresh UEFI variables and a copy of the USB disk as its stick, both in dir.
func withStick(m qemu.Machine, dir string) (qemu.Machine, error) {
	m.Vars = filepath.Join(dir, "OVMF_VARS.fd")
	s := filepath.Join(dir, "stick.raw")
	if err := qemu.FreshVars(m.Vars); err != nil {
		return qemu.Machine{}, err
	}
	if err := qemu.StickCopy(filepath.Join(out, "usb", "disk.raw"), s); err != nil {
		return qemu.Machine{}, err
	}
	m.Sticks = []string{s}
	return m, nil
}

// powerOff shuts the guest down as a person does, and waits for QEMU to end: a machine killed
// mid-write is not the life being tested.
func powerOff(v *qemu.VM, g *guest) {
	_, _ = g.run("systemctl poweroff", "")
	v.Shutdown(3 * time.Minute)
}

// play boots a stick in a window on the host's desktop, with an empty disk to install on: the
// machine a person would use. What it holds - the stick, the disk, the firmware's variables, its TPM -
// is kept in _output/play between runs, so an install, its reboot and its updates carry on.
func play(args []string) error {
	fl := flag.NewFlagSet("play", flag.ExitOnError)
	fresh := fl.Bool("fresh", false, "start over: a new stick from _output/usb/disk.raw, an empty disk, no boot entries")
	withStick := fl.Bool("stick", true, "plug the stick in; without it the machine boots what is installed on its disk")
	mem := fl.String("m", "8G", "memory")
	_ = fl.Parse(args)
	dir := filepath.Join(out, "play")
	stick, nvme, vars := filepath.Join(dir, "stick.raw"), filepath.Join(dir, "nvme.raw"), filepath.Join(dir, "OVMF_VARS.fd")
	if *fresh {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(stick); err != nil {
		src := filepath.Join(out, "usb", "disk.raw")
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("no stick to copy: build one with task usb (%w)", err)
		}
		if err := qemu.StickCopy(src, stick); err != nil {
			return err
		}
	}
	if _, err := os.Stat(nvme); err != nil {
		if err := qemu.Blank(nvme, "64G"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(vars); err != nil {
		if err := qemu.FreshVars(vars); err != nil {
			return err
		}
	}
	// With a TPM, as a laptop has: the install seals /var's key in it.
	m := qemu.Machine{Memory: *mem, Vars: vars, NVMe: nvme, Window: true, TPM: filepath.Join(dir, "tpm"), Dir: dir}
	if *withStick {
		m.Sticks = []string{stick}
	}
	v, err := m.Start()
	if err != nil {
		return err
	}
	return v.Wait()
}
