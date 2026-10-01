// Command image makes E2's disk and boots it: apko's root filesystem, the kernel's signed modules,
// the firmware and image/rootfs merged into one tree - a later layer's file replacing an earlier
// one's - written as an ext4 image by mke2fs straight from the tar, so nothing needs root; then
// QEMU boots it with the kernel given directly, a virtio GPU behind VNC, and QMP to take
// screenshots with.
//
//	image disk  [-size 12G]                     _output/rootfs.tar ... -> _output/disk.raw
//	image boot  [-shot path.ppm -after 90s]     boot it; with -shot, take a screenshot and stop
//	image test  [-only a,b]                     boot the -test disk and drive the session (cmd/image/test.go)
//	image usb   [-test]                         the USB stick's root, kernel image and release, in build/tools as root
//	image usb-disk                              the stick's disk from the signed release (cmd/image/usb.go)
//	image lifecycle                             install, update and roll back in QEMU (cmd/image/lifecycle.go)
//	image run   [-usb] [-wait 30s] -- <script>   boot, run script as root over vsock, stop
//	image check                                 every ELF's NEEDED libraries are in the root
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const out = "_output"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: image disk|boot [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "disk":
		err = disk(os.Args[2:])
	case "boot":
		err = boot(os.Args[2:])
	case "test":
		err = test(os.Args[2:])
	case "usb":
		err = usb(os.Args[2:])
	case "usb-disk":
		err = usbDisk(os.Args[2:])
	case "lifecycle":
		err = lifecycle(os.Args[2:])
	case "run":
		err = runIn(os.Args[2:])
	case "theme":
		err = plymouthTheme()
	case "check":
		err = check(filepath.Join(out, "root.tar"))
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "image:", err)
		os.Exit(1)
	}
}

// layer is one source of files in the root, in the order they are laid down.
type layer interface {
	name() string
	walk(fn func(h *tar.Header, r io.Reader) error) error
}

type tarLayer struct{ file, prefix string }

func (l tarLayer) name() string { return l.file }

func (l tarLayer) walk(fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(l.file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReaderSize(f, 1<<20))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", l.file, err)
		}
		h.Name = clean(path.Join(l.prefix, h.Name))
		if h.Name == "" {
			continue
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

type dirLayer struct{ dir, prefix string }

func (l dirLayer) name() string { return l.dir }

func (l dirLayer) walk(fn func(*tar.Header, io.Reader) error) error {
	return filepath.WalkDir(l.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(l.dir, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		// Everything in the root is root's, whoever built it, and writable by root alone: the
		// checkout's modes carry its umask.
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if h.Typeflag != tar.TypeSymlink {
			h.Mode &^= 0o022
		}
		h.Name = clean(path.Join(l.prefix, filepath.ToSlash(rel)))
		if !info.Mode().IsRegular() {
			return fn(h, nil)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return fn(h, f)
	})
}

// links are the root's symlinks that enable units: what `systemctl enable` and a preset would
// write, which nothing runs at build time.
type links map[string]string

func (l links) name() string { return "enabled units" }

func (l links) walk(fn func(*tar.Header, io.Reader) error) error {
	for name, target := range l {
		h := &tar.Header{Typeflag: tar.TypeSymlink, Name: clean(name), Linkname: target, Mode: 0o777}
		if err := fn(h, nil); err != nil {
			return err
		}
	}
	return nil
}

// keyDir holds the release key's public half at usr/share/spin-desktop/release.pgp (task usb).
var keyDir = filepath.Join(out, "release-key")

// initDir holds cmd/spin-init, built for the root as a layer of its own.
var initDir = filepath.Join(out, "spin-init")

// buildInit builds this repository's programs for the root, static, into initDir: cmd/spin-init
// at /usr/lib/spin-desktop/init, which the USB disk's kernel runs first (init=; the ext4 test
// disk boots systemd directly), and cmd/spin-desktop at /usr/bin/spin-desktop.
func buildInit() error {
	for pkg, dst := range map[string]string{
		"./cmd/spin-init":    "usr/lib/spin-desktop/init",
		"./cmd/spin-desktop": "usr/bin/spin-desktop",
	} {
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(initDir, dst), pkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOAMD64=v3")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func clean(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// rootLayers is what the root is made of, in the order it is laid down; test adds what lets
// cmd/image test drive the session.
func rootLayers(test bool) []layer {
	layers := []layer{
		tarLayer{file: filepath.Join(out, "rootfs.tar")},
		dirLayer{dir: filepath.Join(out, "modules")},
		tarLayer{file: filepath.Join(out, "firmware", "firmware.tar"), prefix: "usr/lib/firmware"},
		dirLayer{dir: "image/rootfs"},
		dirLayer{dir: themeDir},
		dirLayer{dir: initDir},
		links{
			"home": "var/home",
			// Names are resolved by systemd-resolved, which NetworkManager hands each link's DNS to:
			// systemd's tmpfiles points /etc/resolv.conf at its stub.
			"etc/systemd/system/multi-user.target.wants/systemd-resolved.service":        "/usr/lib/systemd/system/systemd-resolved.service",
			"etc/systemd/system/dbus-org.freedesktop.resolve1.service":                   "/usr/lib/systemd/system/systemd-resolved.service",
			"etc/systemd/system/multi-user.target.wants/spin-desktop-boot-check.service": "/usr/lib/systemd/system/spin-desktop-boot-check.service",
			"etc/systemd/system/default.target":                                          "/usr/lib/systemd/system/graphical.target",
			"etc/systemd/system/sysinit.target.wants/spin-desktop-caches.service":        "/usr/lib/systemd/system/spin-desktop-caches.service",
			"etc/localtime": "/usr/share/zoneinfo/America/Argentina/Buenos_Aires",
			"etc/systemd/system/sockets.target.wants/docker.socket":                               "/usr/lib/systemd/system/docker.socket",
			"etc/systemd/system/multi-user.target.wants/containerd.service":                       "/usr/lib/systemd/system/containerd.service",
			"etc/systemd/system/multi-user.target.wants/power-profiles-daemon.service":            "/usr/lib/systemd/system/power-profiles-daemon.service",
			"etc/systemd/system/multi-user.target.wants/spin-desktop-flathub.service":             "/usr/lib/systemd/system/spin-desktop-flathub.service",
			"etc/systemd/system/multi-user.target.wants/NetworkManager-wait-online.service":       "/usr/lib/systemd/system/NetworkManager-wait-online.service",
			"etc/systemd/user/default.target.wants/xdg-user-dirs.service":                         "/usr/lib/systemd/user/xdg-user-dirs.service",
			"etc/systemd/system/multi-user.target.wants/scx.service":                              "/usr/lib/systemd/system/scx.service",
			"etc/systemd/system/sysinit.target.wants/spin-desktop-ssh-hostkeys.service":           "/usr/lib/systemd/system/spin-desktop-ssh-hostkeys.service",
			"etc/systemd/system/display-manager.service":                                          "/usr/lib/systemd/system/gdm.service",
			"etc/systemd/system/multi-user.target.wants/NetworkManager.service":                   "/usr/lib/systemd/system/NetworkManager.service",
			"etc/systemd/system/dbus-org.freedesktop.nm-dispatcher.service":                       "/usr/lib/systemd/system/NetworkManager-dispatcher.service",
			"etc/systemd/system/bluetooth.target.wants/bluetooth.service":                         "/usr/lib/systemd/system/bluetooth.service",
			"etc/systemd/system/dbus-org.freedesktop.Bluetooth.service":                           "/usr/lib/systemd/system/bluetooth.service",
			"etc/systemd/user/pipewire.service.wants/wireplumber.service":                         "/usr/lib/systemd/user/wireplumber.service",
			"etc/systemd/user/sockets.target.wants/pipewire.socket":                               "/usr/lib/systemd/user/pipewire.socket",
			"etc/systemd/user/sockets.target.wants/pipewire-pulse.socket":                         "/usr/lib/systemd/user/pipewire-pulse.socket",
			"etc/systemd/system/sysinit.target.wants/systemd-timesyncd.service":                   "/usr/lib/systemd/system/systemd-timesyncd.service",
			"etc/systemd/system/systemd-networkd-wait-online.service":                             "/dev/null",
			"etc/systemd/system/multi-user.target.wants/systemd-networkd.service":                 "/dev/null",
			"etc/systemd/system/sockets.target.wants/systemd-networkd.socket":                     "/dev/null",
			"etc/systemd/system/network-online.target.wants/systemd-networkd-wait-online.service": "/dev/null",
		},
	}
	// The key a release's signature is checked against, as task usb exported it from the signing
	// keyring: absent from a disk built without one, which then can verify no update.
	if _, err := os.Stat(keyDir); err == nil {
		layers = append(layers, dirLayer{dir: keyDir})
	}
	if test {
		layers = append(layers, dirLayer{dir: "image/test/rootfs"})
	}
	return layers
}

func disk(args []string) error {
	fl := flag.NewFlagSet("disk", flag.ExitOnError)
	size := fl.String("size", "12G", "the ext4 image's size")
	test := fl.Bool("test", false, "add image/test/rootfs: what lets cmd/image test drive the session")
	_ = fl.Parse(args)

	if err := plymouthTheme(); err != nil {
		return err
	}
	if err := buildInit(); err != nil {
		return err
	}
	layers := rootLayers(*test)
	merged := filepath.Join(out, "root.tar")
	if err := merge(layers, merged); err != nil {
		return err
	}
	img := filepath.Join(out, "disk.raw")
	_ = os.Remove(img)
	cmd := exec.Command("mke2fs", "-q", "-t", "ext4", "-L", "root", "-d", merged, img, *size)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mke2fs: %w", err)
	}
	fmt.Println(img)
	return nil
}

// merge writes every layer into one tar, a name laid down by a later layer replacing the earlier
// one's, and every directory a file sits in present before it.
func merge(layers []layer, dst string) error {
	owner := make(map[string]int)
	// A directory's own header, from the layer that has it: a tar may list a directory's files
	// before the directory, and mke2fs needs the directory first.
	dirHdr := make(map[string]tar.Header)
	for i, l := range layers {
		if err := l.walk(func(h *tar.Header, _ io.Reader) error {
			owner[h.Name] = i
			// A directory keeps the first layer's header: the package that made it decides its
			// owner and mode, not an overlay that puts a file in it.
			if h.Typeflag == tar.TypeDir {
				if _, ok := dirHdr[h.Name]; !ok {
					dirHdr[h.Name] = *h
				}
			} else {
				delete(dirHdr, h.Name)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("%s: %w", l.name(), err)
		}
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	tw := tar.NewWriter(bw)
	written := map[string]bool{}
	var dir func(name string) error
	dir = func(name string) error {
		if name == "." || written[name] {
			return nil
		}
		if err := dir(path.Dir(name)); err != nil {
			return err
		}
		written[name] = true
		h, ok := dirHdr[name]
		if !ok {
			h = tar.Header{Typeflag: tar.TypeDir, Mode: 0o755}
		}
		h.Name = name + "/"
		return tw.WriteHeader(&h)
	}
	for i, l := range layers {
		if err := l.walk(func(h *tar.Header, r io.Reader) error {
			if owner[h.Name] != i {
				return nil
			}
			if h.Typeflag == tar.TypeDir {
				return dir(h.Name)
			}
			if err := dir(path.Dir(h.Name)); err != nil {
				return err
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if r != nil && h.Typeflag == tar.TypeReg {
				_, err := io.Copy(tw, r)
				return err
			}
			return nil
		}); err != nil {
			return fmt.Errorf("%s: %w", l.name(), err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return f.Close()
}

func boot(args []string) error {
	fl := flag.NewFlagSet("boot", flag.ExitOnError)
	shot := fl.String("shot", "", "take a screenshot (PPM) here after -after, then stop the VM")
	after := fl.Duration("after", 90*time.Second, "how long after start to take the screenshot")
	mem := fl.String("m", "4G", "memory")
	stick := fl.Bool("usb", false, "boot the USB disk by UEFI from a stick, as a machine does")
	shots := fl.Int("shots", 0, "take this many screenshots, one a second from the start, into _output/boot/, then stop")
	_ = fl.Parse(args)

	v, err := startDisk(*mem, "", *stick)
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

// start boots the disk. With a public key, the guest gets a vsock device for
// systemd-ssh-generator's sshd and the key as ssh.ephemeral-authorized_keys-all, the credential
// that sshd's generated unit authorises - for this boot only, written nowhere on the disk.
func start(mem, pubkey string) (*vm, error) { return startDisk(mem, pubkey, false) }

// startDisk boots _output/disk.raw with the kernel given directly, or with stick the USB disk
// (_output/usb/disk.raw) as a machine boots it: UEFI firmware with variables of its own, the image
// on a USB stick of 32 GB, of which the first boot makes /var.
func startDisk(mem, pubkey string, stick bool) (*vm, error) {
	m := machine{mem: mem, pubkey: pubkey}
	if stick {
		dir := filepath.Join(out, "usb")
		m.vars = filepath.Join(dir, "OVMF_VARS.fd")
		s := filepath.Join(dir, "stick.raw")
		if err := freshVars(m.vars); err != nil {
			return nil, err
		}
		if err := stickCopy(filepath.Join(dir, "disk.raw"), s); err != nil {
			return nil, err
		}
		m.sticks = []string{s}
	}
	return m.start()
}

// machine is a QEMU of this repository's disks: with vars, UEFI firmware keeping its variables in
// that file - boot entries an install made outlive a reboot - and the sticks plugged in, in USB
// order, and an NVMe disk; without, the ext4 test disk with the kernel given directly.
type machine struct {
	mem, pubkey string
	vars        string
	sticks      []string
	nvme        string
}

func (m machine) start() (*vm, error) {
	qmp := filepath.Join(out, "qmp.sock")
	_ = os.Remove(qmp)
	args := []string{"-enable-kvm", "-cpu", "host", "-smp", "4", "-m", m.mem}
	if m.vars != "" {
		args = append(args, "-machine", "q35",
			"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd",
			"-drive", "if=pflash,format=raw,file="+m.vars)
		for i, s := range m.sticks {
			args = append(args, "-drive", fmt.Sprintf("if=none,id=stick%d,format=raw,file=%s", i, s))
		}
		if m.nvme != "" {
			args = append(args, "-drive", "if=none,id=nvme0,format=raw,file="+m.nvme,
				"-device", "nvme,drive=nvme0,serial=spin-desktop-test")
		}
	} else {
		args = append(args,
			"-kernel", filepath.Join(out, "kernel", "bzImage"),
			"-append", "root=/dev/vda rw rootfstype=ext4 console=ttyS0 systemd.firstboot=off",
			"-drive", "file="+filepath.Join(out, "disk.raw")+",if=virtio,format=raw")
	}
	args = append(args,
		"-device", "virtio-vga", "-display", "none", "-vnc", "127.0.0.1:7",
		"-device", "virtio-net-pci,netdev=n0", "-netdev", "user,id=n0",
		"-device", "qemu-xhci,id=xhci", "-device", "usb-tablet",
		// An HDA card, so the sound stack - the kernel's driver, PipeWire, WirePlumber - has a
		// device to find; its output goes nowhere.
		"-audiodev", "none,id=snd0", "-device", "ich9-intel-hda", "-device", "hda-duplex,audiodev=snd0",
		"-serial", "file:"+filepath.Join(out, "serial.log"),
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
	cmd := exec.Command("qemu-system-x86_64", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &vm{cmd: cmd, qmp: qmp, cid: vsockCID}, nil
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

// check reads the merged root and reports every ELF whose NEEDED libraries the dynamic loader
// would not find in it: a package whose dependency was not declared - a provider that publishes
// no so: names, a library in a private directory - shows up here, not as a crash at boot.
func check(root string) error {
	type obj struct {
		needed, runpath []string
	}
	files := map[string]bool{}
	links := map[string]string{}
	objs := map[string]obj{}
	err := tarLayer{file: root}.walk(func(h *tar.Header, r io.Reader) error {
		switch h.Typeflag {
		case tar.TypeSymlink:
			links[h.Name] = h.Linkname
			return nil
		case tar.TypeReg:
		default:
			return nil
		}
		files[h.Name] = true
		if h.Size < 64 {
			return nil
		}
		br := bufio.NewReader(r)
		if b, _ := br.Peek(4); !bytes.Equal(b, []byte(elf.ELFMAG)) {
			return nil
		}
		data, err := io.ReadAll(br)
		if err != nil {
			return err
		}
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		defer f.Close()
		needed, _ := f.DynString(elf.DT_NEEDED)
		rp, _ := f.DynString(elf.DT_RUNPATH)
		rp2, _ := f.DynString(elf.DT_RPATH)
		var runpath []string
		for _, v := range append(rp, rp2...) {
			for _, d := range strings.Split(v, ":") {
				runpath = append(runpath, strings.ReplaceAll(d, "$ORIGIN", "/"+path.Dir(h.Name)))
			}
		}
		if len(needed) > 0 {
			objs[h.Name] = obj{needed, runpath}
		}
		return nil
	})
	if err != nil {
		return err
	}
	exists := func(name string) bool {
		for i := 0; i < 16; i++ {
			if files[name] {
				return true
			}
			t, ok := links[name]
			if !ok {
				return false
			}
			if !strings.HasPrefix(t, "/") {
				t = path.Join(path.Dir(name), t)
			}
			name = clean(t)
		}
		return false
	}
	bad := 0
	for name, o := range objs {
		dirs := append(o.runpath, "/usr/lib", "/lib", "/usr/lib64", "/lib64")
		// Mozilla's applications load their own directory's libraries first, in the order its
		// dependentlibs.list gives, before anything that NEEDs them.
		if files[path.Join(path.Dir(name), "dependentlibs.list")] {
			dirs = append(dirs, "/"+path.Dir(name))
		}
		for _, lib := range o.needed {
			found := false
			for _, d := range dirs {
				if exists(clean(path.Join(d, lib))) {
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("%s: %s not found\n", name, lib)
				bad++
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d unresolved libraries", bad)
	}
	return nil
}
