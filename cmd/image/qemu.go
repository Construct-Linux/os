package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func boot(args []string) error {
	fl := flag.NewFlagSet("boot", flag.ExitOnError)
	shot := fl.String("shot", "", "take a screenshot (PPM) here after -after, then stop the VM")
	after := fl.Duration("after", 90*time.Second, "how long after start to take the screenshot")
	mem := fl.String("m", "4G", "memory")
	stick := fl.Bool("usb", false, "boot the USB disk by UEFI from a stick, as a machine does")
	shots := fl.Int("shots", 0, "take this many screenshots, one a second from the start, into _output/boot/, then stop")
	_ = fl.Parse(args)

	m, err := testMachine(*mem, "", *stick)
	if err != nil {
		return err
	}
	v, err := m.start()
	if err != nil {
		return err
	}
	if *shots > 0 {
		dir := filepath.Join(out, "boot")
		_ = os.RemoveAll(dir)
		_ = os.MkdirAll(dir, 0o755)
		for i := range *shots {
			time.Sleep(time.Second)
			p := filepath.Join(dir, fmt.Sprintf("%02d.ppm", i+1))
			if v.screenshot(p) == nil {
				time.Sleep(300 * time.Millisecond)
				if ppmToPNG(p, strings.TrimSuffix(p, ".ppm")+".png") == nil {
					_ = os.Remove(p)
				}
			}
		}
		v.stop()
		return nil
	}
	fmt.Println("VNC 127.0.0.1:5907, serial", filepath.Join(out, "serial.log"))
	if *shot == "" {
		return v.cmd.Wait()
	}
	time.Sleep(*after)
	err = v.screenshot(*shot)
	v.stop()
	return err
}

// vm is a running QEMU of _output/disk.raw.
type vm struct {
	cmd *exec.Cmd
	qmp string
	cid int
}

// vsockCID is the guest's vsock address: the harness reaches its sshd as vsock/<cid>.
const vsockCID = 4207

// testMachine is the machine that boots _output/disk.raw with the kernel given directly, or with
// stick the USB disk (_output/usb/disk.raw) as a machine boots it: UEFI firmware with variables of
// its own, the image on a USB stick of 32 GB, of which the first boot makes /var. With a public
// key, the guest gets a vsock device for systemd-ssh-generator's sshd and the key as
// ssh.ephemeral-authorized_keys-all, the credential that sshd's generated unit authorises - for
// this boot only, written nowhere.
func testMachine(mem, pubkey string, stick bool) (machine, error) {
	m := machine{mem: mem, pubkey: pubkey}
	if stick {
		dir := filepath.Join(out, "usb")
		m.vars = filepath.Join(dir, "OVMF_VARS.fd")
		s := filepath.Join(dir, "stick.raw")
		if err := freshVars(m.vars); err != nil {
			return machine{}, err
		}
		if err := stickCopy(filepath.Join(dir, "disk.raw"), s); err != nil {
			return machine{}, err
		}
		m.sticks = []string{s}
	}
	return m, nil
}

// machine is a QEMU of this repository's disks: with vars, UEFI firmware keeping its variables in
// that file - boot entries an install made outlive a reboot - and the sticks plugged in, in USB
// order, and an NVMe disk; without, the ext4 test disk with the kernel given directly.
type machine struct {
	mem, pubkey string
	vars        string
	sticks      []string
	nvme        string
	// window shows the machine in a window on the host's desktop, with the GPU's 3D through the
	// host's (virgl) and its sound on the host's speakers: a machine to use, not to test.
	window bool
	// tpm, when set, gives the machine a TPM 2.0 (swtpm) keeping its state in that directory: an
	// install seals /var's key in it, and the next boots unseal it.
	tpm string
	// credentials are systemd credentials for this boot, name=value, as the firmware hands them.
	credentials []string
	// dir is where its QMP socket and serial log go: _output without one; task play's own, so it
	// runs beside a test.
	dir string
	// cmdline are kernel options for this boot, which systemd-stub adds to the kernel image's from
	// the firmware's SMBIOS when Secure Boot is off: a boot to diagnose.
	cmdline []string
}

// machineUUID is the test machine's firmware UUID: the same at every boot, as a board's.
const machineUUID = "5f1d2c3a-7b8e-4c6d-9a0b-1c2d3e4f5a6b"

func (m machine) start() (*vm, error) {
	dir := m.dir
	if dir == "" {
		dir = out
	}
	qmp := filepath.Join(dir, "qmp.sock")
	_ = os.Remove(qmp)
	// A firmware product UUID, as a real board has: systemd.machine_id=firmware makes the
	// machine's id from it, and /var's partition UUID from that, so without one every boot would
	// be another machine's and none would find its /var.
	cpus := "4"
	if m.window {
		cpus = strconv.Itoa(max(4, runtime.NumCPU()/2))
	}
	args := []string{"-enable-kvm", "-cpu", "host", "-smp", cpus, "-m", m.mem, "-uuid", machineUUID}
	if m.vars != "" {
		// Secure Boot's firmware, its variables in setup mode until systemd-boot enrolls the
		// stick's keys (by itself in a VM): every machine with UEFI boots signed or not at all.
		// The variables are written in SMM, which only the firmware may enter.
		args = append(args, "-machine", "q35,smm=on",
			"-global", "driver=cfi.pflash01,property=secure,value=on",
			"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
			"-drive", "if=pflash,format=raw,file="+m.vars)
		for i, s := range m.sticks {
			args = append(args, "-drive", fmt.Sprintf("if=none,id=stick%d,format=raw,file=%s", i, s))
		}
		if m.nvme != "" {
			args = append(args, "-drive", "if=none,id=nvme0,format=raw,file="+m.nvme,
				"-device", "nvme,drive=nvme0,serial=construct-test")
		}
	} else {
		args = append(args,
			"-kernel", filepath.Join(out, "kernel", "bzImage"),
			"-append", "root=/dev/vda rw rootfstype=ext4 console=ttyS0 systemd.firstboot=off",
			"-drive", "file="+filepath.Join(out, "disk.raw")+",if=virtio,format=raw")
	}
	// An HDA card, so the sound stack - the kernel's driver, PipeWire, WirePlumber - has a device
	// to find; its output goes nowhere unless the machine is in a window.
	display := []string{"-device", "virtio-vga", "-display", "none", "-vnc", "127.0.0.1:7", "-audiodev", "none,id=snd0"}
	if m.window {
		display = []string{"-device", "virtio-vga-gl", "-display", "gtk,gl=on,show-cursor=on", "-audiodev", "pipewire,id=snd0"}
	}
	args = append(args, display...)
	args = append(args,
		"-device", "virtio-net-pci,netdev=n0", "-netdev", "user,id=n0",
		"-device", "qemu-xhci,id=xhci", "-device", "usb-tablet",
		"-device", "ich9-intel-hda", "-device", "hda-duplex,audiodev=snd0",
		"-serial", "file:"+filepath.Join(dir, "serial.log"),
		"-qmp", "unix:"+qmp+",server,nowait",
	)
	// After the xHCI controller they plug into; the firmware boots the first stick first.
	for i := range m.sticks {
		dev := fmt.Sprintf("usb-storage,bus=xhci.0,drive=stick%d,removable=on", i)
		if i == 0 && m.nvme == "" {
			dev += ",bootindex=0"
		}
		args = append(args, "-device", dev)
	}
	if m.pubkey != "" {
		args = append(args,
			"-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", vsockCID),
			"-smbios", "type=11,value=io.systemd.credential:ssh.ephemeral-authorized_keys-all="+strings.TrimSpace(m.pubkey))
	}
	for _, c := range m.credentials {
		args = append(args, "-smbios", "type=11,value=io.systemd.credential:"+c)
	}
	if len(m.cmdline) > 0 {
		args = append(args, "-smbios", "type=11,value=io.systemd.stub.kernel-cmdline-extra="+strings.Join(m.cmdline, " "))
	}
	if m.tpm != "" {
		sock, err := startTPM(m.tpm)
		if err != nil {
			return nil, err
		}
		args = append(args, "-chardev", "socket,id=chrtpm,path="+sock,
			"-tpmdev", "emulator,id=tpm0,chardev=chrtpm", "-device", "tpm-crb,tpmdev=tpm0")
	}
	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &vm{cmd: cmd, qmp: qmp, cid: vsockCID}, nil
}

// startTPM starts swtpm, a TPM 2.0 keeping its state in dir, and answers the socket QEMU connects
// to; it ends when QEMU does (--terminate).
func startTPM(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sock := filepath.Join(dir, "swtpm.sock")
	_ = os.Remove(sock)
	cmd := exec.Command("swtpm", "socket", "--tpm2", "--tpmstate", "dir="+dir,
		"--ctrl", "type=unixio,path="+sock, "--terminate")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("swtpm: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	for range 100 {
		if _, err := os.Stat(sock); err == nil {
			return sock, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("swtpm made no socket at %s", sock)
}

// freshVars is a variable store as the firmware ships it: no boot entries.
func freshVars(vars string) error {
	return copyCmd("cp", "/usr/share/OVMF/OVMF_VARS_4M.fd", vars)
}

// stickCopy is the stick a disk image is written onto: 32 GB, sparse past the image, so the first
// boot of it is a first boot and finds room for /var.
func stickCopy(img, stick string) error {
	if err := copyCmd("cp", "--sparse=always", img, stick); err != nil {
		return err
	}
	return copyCmd("truncate", "-s", "32G", stick)
}

func copyCmd(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

func (v *vm) screenshot(file string) error {
	abs, _ := filepath.Abs(file)
	return qmpRun(v.qmp, map[string]any{"execute": "screendump", "arguments": map[string]any{"filename": abs}})
}

// pointTo moves the USB tablet's pointer to (x, y) on QEMU's absolute scale, 0-32767 each way:
// input a locked session wakes its display for, without lifting the lock screen's curtain.
func (v *vm) pointTo(x, y int) error {
	return qmpRun(v.qmp, map[string]any{"execute": "input-send-event", "arguments": map[string]any{"events": []any{
		map[string]any{"type": "abs", "data": map[string]any{"axis": "x", "value": x}},
		map[string]any{"type": "abs", "data": map[string]any{"axis": "y", "value": y}},
	}}})
}

// key presses and releases one key, by QEMU qcode ("shift", "esc").
func (v *vm) key(qcode string) error {
	return qmpRun(v.qmp, map[string]any{"execute": "send-key", "arguments": map[string]any{
		"keys": []any{map[string]any{"type": "qcode", "data": qcode}},
	}})
}

// typeText types text on the machine's keyboard, then Enter: lowercase letters, digits and spaces.
func (v *vm) typeText(text string) error {
	for _, r := range text {
		code := string(r)
		switch {
		case r == ' ':
			code = "spc"
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		default:
			return fmt.Errorf("typeText: no key for %q", r)
		}
		if err := v.key(code); err != nil {
			return err
		}
	}
	return v.key("ret")
}

func (v *vm) stop() {
	_ = qmpRun(v.qmp, map[string]any{"execute": "quit"})
	_ = v.cmd.Wait()
}

func qmpRun(sock string, command map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	dec := json.NewDecoder(c)
	enc := json.NewEncoder(c)
	var greeting map[string]any
	if err := dec.Decode(&greeting); err != nil {
		return err
	}
	for _, m := range []map[string]any{{"execute": "qmp_capabilities"}, command} {
		if err := enc.Encode(m); err != nil {
			return err
		}
		for {
			var resp map[string]any
			if err := dec.Decode(&resp); err != nil {
				if m["execute"] == "quit" {
					return nil
				}
				return err
			}
			if _, ev := resp["event"]; ev {
				continue
			}
			if e, ok := resp["error"]; ok {
				return fmt.Errorf("qmp %v: %v", m["execute"], e)
			}
			break
		}
	}
	return nil
}

// powerOff shuts the guest down as a person does, and waits for QEMU to end: a machine killed
// mid-write is not the life being tested.
func (v *vm) powerOff(g *guest) {
	_, _ = g.run("systemctl poweroff", "")
	done := make(chan struct{})
	go func() { _ = v.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		// The goroutine above is already in Wait; stop() would wait a second time.
		_ = qmpRun(v.qmp, map[string]any{"execute": "quit"})
		<-done
	}
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
		if err := stickCopy(src, stick); err != nil {
			return err
		}
	}
	if _, err := os.Stat(nvme); err != nil {
		if err := copyCmd("truncate", "-s", "64G", nvme); err != nil {
			return err
		}
	}
	if _, err := os.Stat(vars); err != nil {
		if err := freshVars(vars); err != nil {
			return err
		}
	}
	// With a TPM, as a laptop has: the install seals /var's key in it.
	m := machine{mem: *mem, vars: vars, nvme: nvme, window: true, tpm: filepath.Join(dir, "tpm"), dir: dir}
	if *withStick {
		m.sticks = []string{stick}
	}
	v, err := m.start()
	if err != nil {
		return err
	}
	return v.cmd.Wait()
}
