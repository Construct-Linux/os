package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// usb makes _output/usb/disk.raw: a disk a machine boots from a USB stick, laid out as spin-ami's
// bare metal is - an ESP with systemd-boot and a unified kernel image, a read-only EROFS root under
// dm-verity that the kernel sets up itself from the image's command line (no initrd), its hash
// tree, and /var made by systemd-repart at first boot in the rest of the stick. It runs as root in
// build/tools' container: it extracts the root with its owners and runs the image's own programs
// in it.
func usb(args []string) error {
	fl := flag.NewFlagSet("usb", flag.ExitOnError)
	test := fl.Bool("test", false, "add image/test/rootfs, as task disk -- -test does")
	extra := fl.String("append", "", "more of the kernel's command line, to diagnose a boot (plymouth.debug)")
	hashFile := fl.String("password-hash", filepath.Join(".cache", "spin-password.hash"),
		"the user's password, as crypt(3) makes it (openssl passwd -6): the screen locks, and sudo asks")
	_ = fl.Parse(args)

	hash, err := os.ReadFile(*hashFile)
	if err != nil {
		return fmt.Errorf("%w: write one with: openssl passwd -6 > %s", err, *hashFile)
	}
	stage := filepath.Join(out, "usb")
	root := filepath.Join(stage, "root")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := plymouthTheme(); err != nil {
		return err
	}
	// Built on the host by task usb: this container has no Go.
	if _, err := os.Stat(filepath.Join(initDir, "usr/lib/spin-desktop/init")); err != nil {
		return fmt.Errorf("spin-init: %w", err)
	}
	merged := filepath.Join(out, "root.tar")
	if err := merge(rootLayers(*test), merged); err != nil {
		return err
	}
	if err := run("tar", "--numeric-owner", "--xattrs", "-xpf", merged, "-C", root); err != nil {
		return err
	}
	version := time.Now().UTC().Format("20060102.150405")
	if err := bake(root, strings.TrimSpace(string(hash)), version); err != nil {
		return err
	}
	v, err := verifiedRoot(root, filepath.Join(stage, "root.img"), filepath.Join(stage, "root.verity"))
	if err != nil {
		return err
	}
	esp := filepath.Join(stage, "esp")
	label := "spin-desktop_" + version
	v.extra = *extra
	if err := espTree(root, esp, label, v); err != nil {
		return err
	}
	disk := filepath.Join(stage, "disk.raw")
	if err := repart(stage, disk, label, v); err != nil {
		return err
	}
	fmt.Printf("%s: %s, root hash %s\n", disk, label, v.rootHash)
	return nil
}

// bake makes now what a boot would otherwise write into a root it can no longer write: the
// packages' triggers apko does not run, the users sysusers.d declares, the factory files tmpfiles.d
// makes, the hardware database; and the /var partition, /tmp and the user's password.
func bake(root, passwordHash, version string) error {
	// The image's own programs, in the image: they read and write its paths.
	if err := chroot(root,
		"glib-compile-schemas /usr/share/glib-2.0/schemas",
		"gio-querymodules /usr/lib/gio/modules",
		"gdk-pixbuf-query-loaders --update-cache",
		"update-mime-database /usr/share/mime",
		"update-desktop-database -q /usr/share/applications",
		"fc-cache -s",
		"for d in /usr/share/icons/*/; do [ -f \"$d/index.theme\" ] && gtk4-update-icon-cache -q -t -f \"$d\"; done; true",
		"ldconfig",
	); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"systemd-sysusers", "--root=" + root},
		{"systemd-tmpfiles", "--root=" + root, "--create", "--boot",
			"--exclude-prefix=/var", "--exclude-prefix=/run", "--exclude-prefix=/tmp",
			"--exclude-prefix=/dev", "--exclude-prefix=/proc", "--exclude-prefix=/sys", "--exclude-prefix=/home"},
		{"systemd-hwdb", "update", "--root=" + root, "--usr"},
	} {
		if err := run(args[0], args[1:]...); err != nil {
			return err
		}
	}
	if err := setPassword(root, "spin", passwordHash); err != nil {
		return err
	}
	// root has no password and is not locked: "*" rather than "!", which sshd without PAM takes for
	// a locked account and refuses even a key for (spin-ami's keyOnly does the same).
	if err := setPassword(root, "root", "*"); err != nil {
		return err
	}
	files := map[string]string{
		// systemd makes a machine id at every boot, from the firmware's (cmdline), and mounts it here.
		"etc/machine-id": "",
		// What ConditionNeedsUpdate=/etc units (ldconfig, hwdb, the journal's catalog) would do at
		// boot, bake has done: without this file they run at every first boot of an image.
		"etc/.updated": "",
		"etc/fstab": `# / is the kernel's: EROFS under dm-verity, read-only, set up from the command line before
# anything runs (cmd/image usb). What the machine writes goes to /var, the partition
# systemd-repart makes at first boot in the rest of the stick (usr/lib/repart.d/60-var.conf).
PARTLABEL=var /var ext4 defaults 0 2
tmpfs /tmp tmpfs mode=1777,nosuid,nodev 0 0
`,
		"usr/lib/repart.d/60-var.conf": `# /var, made at first boot in whatever the stick has beyond the image.
[Partition]
Type=var
Label=var
Format=ext4
`,
		"usr/lib/os-release": fmt.Sprintf(`NAME="Spin Desktop"
ID=spin-desktop
ID_LIKE=wolfi
PRETTY_NAME="Spin Desktop %[1]s"
VERSION_ID=%[1]s
IMAGE_ID=spin-desktop
IMAGE_VERSION=%[1]s
HOME_URL="https://github.com/spin-stack"
`, version),
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		_ = os.Remove(p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := varSkeleton(root); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(root, "etc/os-release"))
	if err := os.Symlink("../usr/lib/os-release", filepath.Join(root, "etc/os-release")); err != nil {
		return err
	}
	for _, d := range []string{"var", "tmp", "home"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	return os.Chmod(filepath.Join(root, "tmp"), os.ModeSticky|0o777)
}

// varSkeleton records the directories and links the packages put in /var, with their owners and
// modes, as tmpfiles.d lines systemd-tmpfiles makes at every boot: /var is a partition of its own,
// which hides what the image has there (spin-ami's internal/disk does the same). Files are not
// carried; a package that needs one in /var makes it itself.
func varSkeleton(root string) error {
	var b strings.Builder
	b.WriteString("# Written by cmd/image usb: the /var the packages made, which the /var partition hides.\n")
	base := filepath.Join(root, "var")
	err := filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := "/" + strings.TrimPrefix(p, root+"/")
		if p == base || rel == "/var/tmp" {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("%s: no owner", p)
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "L %s - - - - %s\n", rel, t)
		case fi.IsDir():
			mode := uint32(fi.Mode().Perm())
			if fi.Mode()&os.ModeSticky != 0 {
				mode |= 0o1000
			}
			if fi.Mode()&os.ModeSetgid != 0 {
				mode |= 0o2000
			}
			fmt.Fprintf(&b, "d %s %04o %d %d -\n", rel, mode, st.Uid, st.Gid)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "usr/lib/tmpfiles.d/00-var-skeleton.conf"), []byte(b.String()), 0o644)
}

// chroot runs each shell command inside root, with /dev and /proc bound in for the programs that
// open them.
func chroot(root string, cmds ...string) error {
	for _, m := range []string{"dev", "proc"} {
		if err := os.MkdirAll(filepath.Join(root, m), 0o755); err != nil {
			return err
		}
		if err := run("mount", "--rbind", "/"+m, filepath.Join(root, m)); err != nil {
			return err
		}
		defer run("umount", "-R", filepath.Join(root, m))
	}
	for _, c := range cmds {
		if err := run("chroot", root, "/bin/sh", "-c", c); err != nil {
			return fmt.Errorf("in the image, %q: %w", c, err)
		}
	}
	return nil
}

// setPassword puts hash in /etc/shadow for user: on a read-only root nothing can set it later.
func setPassword(root, user, hash string) error {
	if hash != "*" && !strings.HasPrefix(hash, "$") {
		return fmt.Errorf("the password hash is not crypt(3)'s (openssl passwd -6)")
	}
	p := filepath.Join(root, "etc/shadow")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		f := strings.Split(l, ":")
		if len(f) > 2 && f[0] == user {
			f[1] = hash
			lines[i] = strings.Join(f, ":")
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s has no line in /etc/shadow", user)
	}
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o640)
}

// verity is what the kernel needs to check the root, as spin-ami's internal/disk has it.
type verity struct {
	dataBlocks       int64
	rootHash, salt   string
	rootUUID, vUUID  string
	rootImg, treeImg string
	extra            string
}

const verityBlock = 4096

var rootHashLine = regexp.MustCompile(`(?m)^Root hash:\s+([0-9a-f]{64})$`)

// verifiedRoot writes root as an EROFS image, and its dm-verity tree to a file of its own for a
// partition of its own; the salt is the image's SHA-256, so the same tree is the same root.
func verifiedRoot(root, img, tree string) (verity, error) {
	if err := run("mkfs.erofs", "-zlz4hc", "-T0", "--ignore-mtime", img, root); err != nil {
		return verity{}, err
	}
	fi, err := os.Stat(img)
	if err != nil {
		return verity{}, err
	}
	if fi.Size()%verityBlock != 0 {
		return verity{}, fmt.Errorf("the root image is %d bytes, not whole %d-byte blocks", fi.Size(), verityBlock)
	}
	f, err := os.Open(img)
	if err != nil {
		return verity{}, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return verity{}, err
	}
	salt := hex.EncodeToString(h.Sum(nil))
	if err := os.WriteFile(tree, nil, 0o644); err != nil {
		return verity{}, err
	}
	blocks := fi.Size() / verityBlock
	outb, err := exec.Command("veritysetup", "format", img, tree, "--no-superblock", "--hash=sha256",
		fmt.Sprintf("--data-block-size=%d", verityBlock), fmt.Sprintf("--hash-block-size=%d", verityBlock),
		fmt.Sprintf("--data-blocks=%d", blocks), "--salt="+salt).CombinedOutput()
	if err != nil {
		return verity{}, fmt.Errorf("veritysetup: %w: %s", err, outb)
	}
	m := rootHashLine.FindSubmatch(outb)
	if m == nil {
		return verity{}, fmt.Errorf("veritysetup gave no root hash: %s", outb)
	}
	rh := string(m[1])
	uuid := func(h string) string {
		return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	}
	return verity{dataBlocks: blocks, rootHash: rh, salt: salt, rootUUID: uuid(rh), vUUID: uuid(rh[32:]),
		rootImg: img, treeImg: tree}, nil
}

// cmdline is the kernel's command line, in the kernel image where nothing at boot can change it.
// Plymouth draws on the firmware's framebuffer (simpledrm) from the start: the GPU's own driver
// is a module udev loads seconds later, and without plymouth.use-simpledrm Plymouth waits for it
// and the boot shows text instead. And with a serial console on the command line Plymouth runs as
// text on every console and looks for no screen at all, unless told to leave serial ones out.
//
// spin-ami's bare metal's root - dm-verity over the root partition, checked against the tree
// partition, both found by the partition UUIDs the root hash gives them - and its consoles, the
// machine's id from its firmware, cgroup v2 only, and a reboot ten seconds after a panic.
func (v verity) cmdline() string {
	data, hash := "PARTUUID="+v.rootUUID, "PARTUUID="+v.vUUID
	table := fmt.Sprintf("0 %d verity 1 %s %s %d %d %d 0 sha256 %s %s 1 panic_on_corruption",
		v.dataBlocks*verityBlock/512, data, hash, verityBlock, verityBlock, v.dataBlocks, v.rootHash, v.salt)
	return `dm-mod.create="root,,,ro,` + table + `" dm-mod.waitfor=` + data + "," + hash +
		" root=/dev/dm-0 rootfstype=erofs ro init=/usr/lib/spin-desktop/init" +
		" console=ttyS0,115200n8 console=tty0 systemd.gpt_auto=0 cgroup_no_v1=all,named" +
		" systemd.machine_id=firmware panic=10 quiet loglevel=3 splash plymouth.use-simpledrm plymouth.ignore-serial-consoles" + strings.TrimRight(" "+v.extra, " ")
}

// espTree lays the ESP: systemd-boot at the removable-media path, which firmware boots from a
// stick with no boot entry, and the kernel image with the microcode both vendors' processors load
// before the kernel runs.
func espTree(root, esp, label string, v verity) error {
	boot, err := os.ReadFile(filepath.Join(root, "usr/lib/systemd/boot/efi/systemd-bootx64.efi"))
	if err != nil {
		return fmt.Errorf("the image's systemd-boot: %w", err)
	}
	for p, b := range map[string][]byte{
		"EFI/BOOT/BOOTX64.EFI":            boot,
		"EFI/systemd/systemd-bootx64.efi": boot,
		"loader/loader.conf":              []byte("timeout 3\neditor no\n"),
	} {
		full := filepath.Join(esp, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(esp, "EFI", "Linux"), 0o755); err != nil {
		return err
	}
	ucode, err := microcode(filepath.Join(filepath.Dir(esp), "microcode.cpio"))
	if err != nil {
		return err
	}
	shown, err := splash(splashLine{"Spin Desktop", 4}, splashLine{strings.TrimPrefix(label, "spin-desktop_"), 2})
	if err != nil {
		return err
	}
	bmpFile := filepath.Join(filepath.Dir(esp), "splash.bmp")
	if err := os.WriteFile(bmpFile, shown, 0o644); err != nil {
		return err
	}
	return run("ukify", "build",
		"--splash", bmpFile,
		"--linux", filepath.Join(out, "kernel", "bzImage"),
		"--initrd", ucode,
		"--cmdline", v.cmdline(),
		"--os-release", "@"+filepath.Join(root, "usr/lib/os-release"),
		"--uname", strings.TrimSpace(readFile(filepath.Join(out, "kernel", "release"))),
		"--output", filepath.Join(esp, "EFI", "Linux", label+".efi"))
}

// microcode is the early cpio the kernel takes its processors' microcode from before it runs
// anything: Intel's and AMD's, from the firmware the desktop carries.
func microcode(dst string) (string, error) {
	dst, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	work := filepath.Join(filepath.Dir(dst), "ucode")
	dir := filepath.Join(work, "kernel/x86/microcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := run("tar", "-xf", filepath.Join(out, "firmware", "firmware.tar"), "-C", work,
		"--wildcards", "./intel-ucode/*", "./amd-ucode/*"); err != nil {
		return "", err
	}
	for name, glob := range map[string]string{"GenuineIntel.bin": "intel-ucode/*", "AuthenticAMD.bin": "amd-ucode/microcode_amd*.bin*"} {
		files, _ := filepath.Glob(filepath.Join(work, glob))
		sort.Strings(files)
		var all bytes.Buffer
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			if strings.HasSuffix(f, ".zst") {
				return "", fmt.Errorf("%s: microcode must not be compressed", f)
			}
			all.Write(b)
		}
		if err := os.WriteFile(filepath.Join(dir, name), all.Bytes(), 0o644); err != nil {
			return "", err
		}
	}
	cmd := exec.Command("sh", "-c", "find kernel | sort | cpio -o -H newc --quiet --reproducible > "+shellQuote(dst))
	cmd.Dir = work
	cmd.Stderr = os.Stderr
	return dst, cmd.Run()
}

// repart lays the stick's image: the ESP, the root and its tree; /var is the stick's own, made at
// first boot. The seed is the root's partition UUID, so the same root is the same disk.
func repart(stage, disk, label string, v verity) error {
	defs := filepath.Join(stage, "repart.d")
	if err := os.MkdirAll(defs, 0o755); err != nil {
		return err
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	for name, body := range map[string]string{
		"10-esp.conf": fmt.Sprintf("[Partition]\nType=esp\nLabel=esp\nFormat=vfat\nCopyFiles=%s:/\nSizeMinBytes=512M\nSizeMaxBytes=512M\n",
			abs(filepath.Join(stage, "esp"))),
		"20-root.conf": fmt.Sprintf("[Partition]\nType=root\nLabel=%s\nUUID=%s\nCopyBlocks=%s\nReadOnly=yes\n",
			label, v.rootUUID, abs(v.rootImg)),
		"21-root-verity.conf": fmt.Sprintf("[Partition]\nType=root-verity\nLabel=%s\nUUID=%s\nCopyBlocks=%s\nReadOnly=yes\n",
			label, v.vUUID, abs(v.treeImg)),
	} {
		if err := os.WriteFile(filepath.Join(defs, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return run("systemd-repart", "--empty=create", "--size=auto", "--dry-run=no", "--no-pager",
		"--definitions="+defs, "--seed="+v.rootUUID, disk)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
