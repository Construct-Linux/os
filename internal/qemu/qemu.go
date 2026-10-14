// Package qemu runs the machines the harness tests on: a QEMU with UEFI firmware and its own
// variables, sticks, an NVMe disk, a TPM and a security key as asked, reached over QMP - the
// screen, the keyboard, the pointer, power - and over vsock, the guest's sshd. The harness's own
// QEMU (image task harness-qemu, with CanoKey) runs in a container when it is built; the host's
// otherwise.
package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultCID is the guest's vsock address unless a Machine names another: the harness reaches its
// sshd as vsock/<cid>.
const DefaultCID = 4207

// uuid is every machine's firmware UUID: the same at every boot, as a board's. systemd's
// machine_id=firmware makes the machine's id from it, and /var's partition UUID from that, so
// without one every boot would be another machine's and none would find its /var.
const uuid = "5f1d2c3a-7b8e-4c6d-9a0b-1c2d3e4f5a6b"

// Machine is a QEMU: with Vars, UEFI firmware keeping its variables in that file - boot entries an
// install made outlive a reboot - and the Sticks plugged in, in USB order, and an NVMe disk;
// without, Disk with Kernel given directly.
type Machine struct {
	Memory string
	// Dir is where its QMP socket and serial log go.
	Dir string
	// Kernel and Disk are what a machine without Vars boots: the kernel given directly, the ext4
	// disk its root.
	Kernel, Disk string
	Vars         string
	Sticks       []string
	NVMe         string
	// Window shows the machine in a window on the host's desktop, with the GPU's 3D through the
	// host's (virgl) and its sound on the host's speakers: a machine to use, not to test.
	Window bool
	// TPM, when set, gives the machine a TPM 2.0 (swtpm) keeping its state in that directory: an
	// install seals /var's key in it, and the next boots unseal it.
	TPM string
	// SecurityKey, when set, plugs a FIDO2 security key into the machine: QEMU's CanoKey, a whole
	// authenticator (PIN, hmac-secret) in emulation, keeping its state in that file across boots.
	SecurityKey string
	// PubKey, when set, gives the guest a vsock device for systemd-ssh-generator's sshd and the key
	// as ssh.ephemeral-authorized_keys-all, the credential sshd's generated unit authorises - for
	// this boot only, written nowhere.
	PubKey string
	// Credentials are systemd credentials for this boot, name=value, as the firmware hands them.
	Credentials []string
	// Cmdline are kernel options for this boot, which systemd-stub adds to the kernel image's from
	// the firmware's SMBIOS when Secure Boot is off: a boot to diagnose.
	Cmdline []string
	// Width and Height are the virtio GPU's panel; 0 is QEMU's default, 1280x800.
	Width, Height int
	// CID is the guest's vsock address, DefaultCID without one.
	CID int
	// VNC serves the screen at 127.0.0.1:5907 for a person to watch: task boot's. The harness
	// takes its screenshots over QMP and opens no port.
	VNC bool
}

// VM is a running QEMU, or one Attach found running.
type VM struct {
	cmd *exec.Cmd
	// stopTPM ends the swtpm this process started for its QEMU, once QEMU has ended: swtpm ends
	// by itself with the QEMU that connected to it (--terminate), and this is for one that never
	// did - a QEMU that refused a later argument - which would otherwise wait until the harness
	// exits.
	stopTPM func()
	// QMP is its QMP socket.
	QMP string
	// CID is its guest's vsock address.
	CID int
}

// Attach is a VM another process started, by its QMP socket: its screen, its keyboard, and Stop.
func Attach(qmp string, cid int) *VM {
	return &VM{QMP: qmp, CID: cid}
}

// Start starts m.
func (m Machine) Start() (*VM, error) {
	if m.Dir == "" {
		return nil, errors.New("qemu: a machine without a Dir has nowhere for its QMP socket")
	}
	qmp := filepath.Join(m.Dir, "qmp.sock")
	_ = os.Remove(qmp)
	cid := m.CID
	if cid == 0 {
		cid = DefaultCID
	}
	cpus := "4"
	if m.Window {
		cpus = strconv.Itoa(max(4, runtime.NumCPU()/2))
	}
	// -nodefaults: only the devices named here - QEMU's own would add a DVD drive a PC no longer
	// has, which libata probes and fails to power-check after a resume.
	args := []string{"-nodefaults", "-enable-kvm", "-cpu", "host", "-smp", cpus, "-m", m.Memory, "-uuid", uuid}
	if m.Vars != "" {
		// Secure Boot's firmware, its variables in setup mode until systemd-boot enrolls the
		// stick's keys (by itself in a VM): every machine with UEFI boots signed or not at all.
		// The variables are written in SMM, which only the firmware may enter.
		args = append(args, "-machine", "q35,smm=on",
			"-global", "driver=cfi.pflash01,property=secure,value=on",
			"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
			"-drive", "if=pflash,format=raw,file="+m.Vars)
		for i, s := range m.Sticks {
			args = append(args, "-drive", fmt.Sprintf("if=none,id=stick%d,format=raw,file=%s", i, s))
		}
		if m.NVMe != "" {
			args = append(args, "-drive", "if=none,id=nvme0,format=raw,file="+m.NVMe,
				"-device", "nvme,drive=nvme0,serial=construct-test")
		}
	} else {
		args = append(args,
			"-kernel", m.Kernel,
			"-append", "root=/dev/vda rw rootfstype=ext4 console=ttyS0 systemd.firstboot=off",
			"-drive", "file="+m.Disk+",if=virtio,format=raw")
	}
	// An HDA card, so the sound stack - the kernel's driver, PipeWire, WirePlumber - has a device
	// to find; its output goes nowhere unless the machine is in a window.
	display := []string{"-device", m.gpu(), "-display", "none", "-audiodev", "none,id=snd0"}
	if m.VNC {
		display = append(display, "-vnc", "127.0.0.1:7")
	}
	if m.Window {
		display = []string{"-device", "virtio-vga-gl", "-display", "gtk,gl=on,show-cursor=on", "-audiodev", "pipewire,id=snd0"}
	}
	args = append(args, display...)
	args = append(args,
		"-device", "virtio-net-pci,netdev=n0", "-netdev", "user,id=n0",
		"-device", "qemu-xhci,id=xhci", "-device", "usb-tablet",
		"-device", "ich9-intel-hda", "-device", "hda-duplex,audiodev=snd0",
		"-serial", "file:"+filepath.Join(m.Dir, "serial.log"),
		"-qmp", "unix:"+qmp+",server,nowait",
	)
	// After the xHCI controller they plug into; the firmware boots the first stick first.
	for i := range m.Sticks {
		dev := fmt.Sprintf("usb-storage,bus=xhci.0,drive=stick%d,removable=on", i)
		if i == 0 && m.NVMe == "" {
			dev += ",bootindex=0"
		}
		args = append(args, "-device", dev)
	}
	if m.SecurityKey != "" {
		args = append(args, "-device", "canokey,bus=xhci.0,file="+m.SecurityKey)
	}
	if m.PubKey != "" {
		args = append(args,
			"-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", cid),
			"-smbios", "type=11,value=io.systemd.credential:ssh.ephemeral-authorized_keys-all="+strings.TrimSpace(m.PubKey))
	}
	for _, c := range m.Credentials {
		args = append(args, "-smbios", "type=11,value=io.systemd.credential:"+c)
	}
	if len(m.Cmdline) > 0 {
		args = append(args, "-smbios", "type=11,value=io.systemd.stub.kernel-cmdline-extra="+strings.Join(m.Cmdline, " "))
	}
	inContainer := harnessBuilt(m.Window)
	stopTPM := func() {}
	if m.TPM != "" {
		// The harness's QEMU starts swtpm beside it (build/qemu/qemu.sh); the host's needs one here.
		sock := filepath.Join(m.TPM, "swtpm.sock")
		if !inContainer {
			var err error
			if sock, stopTPM, err = startTPM(m.TPM); err != nil {
				return nil, err
			}
		}
		args = append(args, "-chardev", "socket,id=chrtpm,path="+sock,
			"-tpmdev", "emulator,id=tpm0,chardev=chrtpm", "-device", "tpm-crb,tpmdev=tpm0")
	}
	cmd := command(inContainer, m.TPM, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		// swtpm ends with the QEMU that connects to it (--terminate), and none will.
		stopTPM()
		return nil, err
	}
	return &VM{cmd: cmd, stopTPM: stopTPM, QMP: qmp, CID: cid}, nil
}

// gpu is QEMU's display device for the panel: virtio-vga's xres and yres are what the virtio GPU
// reports to the guest (VIRTIO_GPU_CMD_GET_DISPLAY_INFO) and the preferred mode of the EDID QEMU
// generates for it, which the kernel's virtio_gpu offers mutter.
func (m Machine) gpu() string {
	if m.Width == 0 {
		return "virtio-vga"
	}
	return fmt.Sprintf("virtio-vga,xres=%d,yres=%d", m.Width, m.Height)
}

// startTPM starts swtpm, a TPM 2.0 keeping its state in dir, and answers the socket QEMU connects
// to and how to stop it. It ends when QEMU does (--terminate), so only a QEMU that never connects
// needs stop: swtpm would otherwise wait for it until the harness exits.
func startTPM(dir string) (sock string, stop func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	sock = filepath.Join(dir, "swtpm.sock")
	_ = os.Remove(sock)
	cmd := exec.Command("swtpm", "socket", "--tpm2", "--tpmstate", "dir="+dir,
		"--ctrl", "type=unixio,path="+sock, "--terminate")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("swtpm: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	stop = func() {
		_ = cmd.Process.Kill() // fails only when it already ended
		<-done
	}
	for range 100 {
		if _, err := os.Stat(sock); err == nil {
			return sock, stop, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("swtpm made no socket at %s", sock)
}

// FreshVars writes a variable store as the firmware ships it at vars: no boot entries.
func FreshVars(vars string) error {
	return run("cp", "/usr/share/OVMF/OVMF_VARS_4M.fd", vars)
}

// StickCopy writes the stick a disk image is written onto: 32 GB, sparse past the image, so the
// first boot of it is a first boot and finds room for /var.
func StickCopy(img, stick string) error {
	if err := run("cp", "--sparse=always", img, stick); err != nil {
		return err
	}
	return run("truncate", "-s", "32G", stick)
}

// Blank writes an empty disk of 64 GB at path, sparse: one to install on.
func Blank(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(64 << 30); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func run(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

// harnessImage is the image task harness-qemu builds: QEMU with CanoKey, which the host's lacks.
const harnessImage = "construct-qemu"

// harnessBuilt is whether machines run in the harness's QEMU: it is built, and the machine is not
// in a window (task play), which the container has no display for.
func harnessBuilt(window bool) bool {
	return !window && exec.Command("docker", "image", "inspect", harnessImage).Run() == nil
}

// command is QEMU with args: the harness's own (task harness-qemu) in a container - one that sees
// the repository at the same path, the host's KVM, vsock and network (the lifecycle's HTTP server
// at 10.0.2.2 is the host's), the host's UEFI firmware, and the TPM in tpm, whose swtpm it starts
// itself - or the host's QEMU. The caller decides which once (harnessBuilt), since the TPM it
// starts depends on it too.
func command(inContainer bool, tpm string, args ...string) *exec.Cmd {
	if !inContainer {
		return exec.Command("qemu-system-x86_64", args...)
	}
	wd, _ := os.Getwd()
	run := []string{"run", "--rm", "-i", "--network", "host", "-e", "SWTPM_STATE=" + tpm,
		"--device", "/dev/kvm", "--device", "/dev/vhost-vsock",
		"-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--group-add", groupOf("/dev/kvm"),
		"-v", wd + ":" + wd, "-w", wd, "-v", "/usr/share/OVMF:/usr/share/OVMF:ro", harnessImage}
	return exec.Command("docker", append(run, args...)...)
}

// groupOf is the group that owns path, by number: the container has no names for the host's.
func groupOf(path string) string {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "0"
	}
	return strconv.Itoa(int(st.Gid))
}

// HasCanoKey is whether the QEMU the harness runs was built with CanoKey (libcanokey-qemu), the
// emulated security key the lifecycle opens /var with; most distributions' QEMU is not.
func HasCanoKey() bool {
	out, err := command(harnessBuilt(false), "", "-device", "help").CombinedOutput()
	return err == nil && strings.Contains(string(out), `"canokey"`)
}

// Wait waits for QEMU to end. A VM Attach found is another process's to wait for.
func (v *VM) Wait() error {
	if v.cmd == nil {
		return errors.New("qemu: a VM found by Attach is not this process's to wait for")
	}
	err := v.cmd.Wait()
	v.ended()
	return err
}

// Stop ends QEMU at once, a power cut: what the guest had not synced is lost. It waits for a QEMU
// this process started to end; one Attach found ends on its own. The error is the quit's: a QEMU
// that was not there.
func (v *VM) Stop() error {
	err := qmp(v.QMP, map[string]any{"execute": "quit"})
	if v.cmd != nil {
		_ = v.cmd.Wait()
		v.ended()
	}
	return err
}

// Shutdown waits up to d for QEMU to end - the guest asked to power off - and then ends it: a
// quit, and a kill when even that is not taken, or the caller would wait on it for good.
func (v *VM) Shutdown(d time.Duration) {
	if v.cmd == nil {
		_ = v.Stop()
		return
	}
	done := make(chan struct{})
	go func() { _ = v.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		// The goroutine above is already in Wait; Stop would wait a second time.
		_ = qmp(v.QMP, map[string]any{"execute": "quit"})
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = v.cmd.Process.Kill()
			<-done
		}
	}
	v.ended()
}

// ended is what follows QEMU's end: the swtpm started for it, if it never took it, goes too.
func (v *VM) ended() {
	if v.stopTPM != nil {
		v.stopTPM()
	}
}

// Screendump writes the screen to file as QEMU's PPM.
func (v *VM) Screendump(file string) error {
	abs, _ := filepath.Abs(file)
	return qmp(v.QMP, map[string]any{"execute": "screendump", "arguments": map[string]any{"filename": abs}})
}

// PNG saves the screen as a PNG at path, QEMU's own (the harness's QEMU is built with PNG, as
// distributions' are). Every screen the harness keeps for a person goes through here, so it is
// checked in one place.
func (v *VM) PNG(path string) error {
	abs, _ := filepath.Abs(path)
	if err := qmp(v.QMP, map[string]any{"execute": "screendump", "arguments": map[string]any{"filename": abs, "format": "png"}}); err != nil {
		return err
	}
	// QMP's answer is not taken as the file being whole: a screen read short would be measured as
	// something it is not, so it is decoded whole before anything reads it - and again, a moment
	// later, the few times it is not whole yet.
	var err error
	for range 10 {
		if err = decodePNG(abs); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("screendump %s: %w", path, err)
}

func decodePNG(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = png.Decode(f)
	return err
}

// PointTo moves the USB tablet's pointer to (x, y) on QEMU's absolute scale, 0-32767 each way:
// input a locked session wakes its display for, without lifting the lock screen's curtain.
func (v *VM) PointTo(x, y int) error {
	return qmp(v.QMP, map[string]any{"execute": "input-send-event", "arguments": map[string]any{"events": []any{
		map[string]any{"type": "abs", "data": map[string]any{"axis": "x", "value": x}},
		map[string]any{"type": "abs", "data": map[string]any{"axis": "y", "value": y}},
	}}})
}

// Button presses (down) or releases the USB tablet's left button where the pointer is. A press
// held while the screen is shot is how a view shows GTK's :active, which no key and no pointer
// move draws; released elsewhere, it clicks nothing.
func (v *VM) Button(down bool) error {
	return qmp(v.QMP, map[string]any{"execute": "input-send-event", "arguments": map[string]any{"events": []any{
		map[string]any{"type": "btn", "data": map[string]any{"down": down, "button": "left"}},
	}}})
}

// PlugUSB plugs the disk image file in as a USB stick, named id, as a person plugs one into a
// running machine: QEMU opens it and puts a usb-storage device on the xHCI bus. file must be under
// the repository, which is all QEMU's container sees of the host.
func (v *VM) PlugUSB(id, file string) error {
	if err := qmp(v.QMP, map[string]any{"execute": "blockdev-add", "arguments": map[string]any{
		"node-name": id, "driver": "raw", "file": map[string]any{"driver": "file", "filename": file},
	}}); err != nil {
		return err
	}
	return qmp(v.QMP, map[string]any{"execute": "device_add", "arguments": map[string]any{
		"driver": "usb-storage", "bus": "xhci.0", "drive": id, "id": id, "removable": true,
	}})
}

// UnplugUSB pulls out the stick PlugUSB plugged in as id.
func (v *VM) UnplugUSB(id string) error {
	return qmp(v.QMP, map[string]any{"execute": "device_del", "arguments": map[string]any{"id": id}})
}

// Key presses and releases a key, or a chord of them held together, by QEMU qcode ("shift",
// "esc"; "ctrl", "comma").
func (v *VM) Key(qcodes ...string) error {
	keys := make([]any, len(qcodes))
	for i, q := range qcodes {
		keys[i] = map[string]any{"type": "qcode", "data": q}
	}
	return qmp(v.QMP, map[string]any{"execute": "send-key", "arguments": map[string]any{"keys": keys}})
}

// TypeText types text on the machine's keyboard, then Enter: lowercase letters, digits and spaces.
func (v *VM) TypeText(text string) error {
	for _, r := range text {
		code := string(r)
		switch {
		case r == ' ':
			code = "spc"
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		default:
			return fmt.Errorf("TypeText: no key for %q", r)
		}
		if err := v.Key(code); err != nil {
			return err
		}
	}
	return v.Key("ret")
}

// qmp sends command over the QMP socket sock, after the capabilities' negotiation, and waits for
// its answer, skipping the events QEMU sends meanwhile. A quit is answered by the socket closing.
func qmp(sock string, command map[string]any) error {
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
