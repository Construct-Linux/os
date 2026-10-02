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
	broken := fl.Bool("broken", false, "add image/test/broken: a release whose GDM fails, for the lifecycle test's rollback")
	_ = fl.Parse(args)

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
	if err := writeTransfers(); err != nil {
		return err
	}
	// Built on the host by task usb: this container has no Go.
	if _, err := os.Stat(filepath.Join(initDir, "usr/lib/spindrift/init")); err != nil {
		return fmt.Errorf("spin-init: %w", err)
	}
	merged := filepath.Join(out, "root.tar")
	layers := rootLayers(*test)
	if *broken {
		layers = append(layers, dirLayer{dir: "image/test/broken"})
	}
	if err := merge(layers, merged); err != nil {
		return err
	}
	if err := run("tar", "--numeric-owner", "--xattrs", "-xpf", merged, "-C", root); err != nil {
		return err
	}
	version := time.Now().UTC().Format("20060102.150405")
	if err := bake(root, version); err != nil {
		return err
	}
	v, err := verifiedRoot(root, filepath.Join(stage, "root.img"), filepath.Join(stage, "root.verity"))
	if err != nil {
		return err
	}
	esp := filepath.Join(stage, "esp")
	label := "spindrift_" + version
	v.extra = *extra
	if err := espTree(root, esp, label, v); err != nil {
		return err
	}
	rel, err := writeRelease(version, esp, label, v)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s, root hash %s; sign SHA256SUMS, then image usb-disk\n", rel, label, v.rootHash)
	return nil
}

// releaseDir is where a version's release is written: what an update reads, and what the stick's
// spin-update partition carries.
func releaseDir(version string) string { return filepath.Join(out, "release", version) }

// writeRelease lays version's release: the root and its tree named with the partition UUIDs a
// slot takes for them (the kernel image's command line names those), the kernel image, and
// SHA256SUMS over them, which the host signs (SHA256SUMS.gpg, gpg --detach-sign: systemd-sysupdate's name for it) before the stick
// is made. internal/update reads it by these names.
func writeRelease(version, esp, label string, v verity) (string, error) {
	dir := releaseDir(version)
	// One release at a time: every reader takes the newest (task usb signs it, usb-disk lays it,
	// test:lifecycle copies each version out), and at 2 GB a build, keeping them filled the disk.
	if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// The root and its tree compressed - what is downloaded, and what GitHub takes (2 GB a file
	// at most); systemd-import and drift install decompress them as they write.
	files := map[string]string{
		label + "_" + v.rootUUID + ".root.raw.xz": v.rootImg,
		label + "_" + v.vUUID + ".verity.raw.xz":  v.treeImg,
		label + ".efi":                            filepath.Join(esp, "EFI", "Linux", label+".efi"),
	}
	names := make([]string, 0, len(files))
	for name, src := range files {
		dst := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".xz") {
			if err := run("sh", "-c", "xz -T0 -6 -c "+shellQuote(src)+" > "+shellQuote(dst)); err != nil {
				return "", err
			}
		} else if err := os.Link(src, dst); err != nil {
			return "", err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sums, "%x  %s\n", h.Sum(nil), name)
	}
	return dir, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644)
}

// usbDisk lays the stick's image from the last usb's stage once its release is signed: the ESP,
// the root and its tree, and spin-update, a FAT partition holding the release - what a machine
// installs from, and updates from when the stick is plugged into one already installed.
func usbDisk(args []string) error {
	fl := flag.NewFlagSet("usb-disk", flag.ExitOnError)
	version := fl.String("version", "", "the release to lay (default: the newest in _output/release)")
	_ = fl.Parse(args)
	if *version == "" {
		entries, err := os.ReadDir(filepath.Join(out, "release"))
		if err != nil || len(entries) == 0 {
			return fmt.Errorf("no release in %s: run image usb first", filepath.Join(out, "release"))
		}
		*version = entries[len(entries)-1].Name()
	}
	rel := releaseDir(*version)
	if _, err := os.Stat(filepath.Join(rel, "SHA256SUMS.gpg")); err != nil {
		return fmt.Errorf("%s is not signed: %w", rel, err)
	}
	label := "spindrift_" + *version
	stage := filepath.Join(out, "usb")
	// The stick boots the uncompressed root and tree the build left in its stage; the release
	// names the partition UUIDs they take.
	v := verity{rootImg: filepath.Join(stage, "root.img"), treeImg: filepath.Join(stage, "root.verity")}
	matches, _ := filepath.Glob(filepath.Join(rel, label+"_*.root.raw.xz"))
	vm, _ := filepath.Glob(filepath.Join(rel, label+"_*.verity.raw.xz"))
	if len(matches) != 1 || len(vm) != 1 {
		return fmt.Errorf("%s has no single root and verity", rel)
	}
	v.rootUUID = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(matches[0]), label+"_"), ".root.raw.xz")
	v.vUUID = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(vm[0]), label+"_"), ".verity.raw.xz")
	disk := filepath.Join(stage, "disk.raw")
	_ = os.Remove(disk)
	if err := repart(stage, disk, label, v, rel); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", disk, label)
	return nil
}

// bake makes now what a boot would otherwise write into a root it can no longer write: the
// packages' triggers apko does not run, the users sysusers.d declares, the factory files tmpfiles.d
// makes, the hardware database; and the /var partition, /tmp and the user's password.
func bake(root, version string) error {
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
		// The desktop's defaults (etc/dconf/db/local.d): theme, icons, accent, extensions.
		"dconf update",
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
# anything runs (cmd/image usb). /var is mounted by spin-init (cmd/spin-init), from the root's own
# disk, before systemd starts; it lays /etc over it.
tmpfs /tmp tmpfs mode=1777,nosuid,nodev 0 0
`,
		"usr/lib/repart.d/60-var.conf": `# /var, made by spin-init at a stick's first boot in whatever it has beyond the image.
[Partition]
Type=var
Label=var
Format=ext4
`,
		"usr/lib/os-release": fmt.Sprintf(`NAME="Spindrift"
ID=spindrift
ID_LIKE=wolfi
PRETTY_NAME="Spindrift %[1]s"
VERSION_ID=%[1]s
IMAGE_ID=spindrift
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
	// spin-init makes /var on the root's disk; systemd-repart's own unit would look at every
	// disk's free space, an installed machine's included.
	_ = os.Remove(filepath.Join(root, "etc/systemd/system/systemd-repart.service"))
	if err := os.Symlink("/dev/null", filepath.Join(root, "etc/systemd/system/systemd-repart.service")); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(root, "etc/os-release"))
	if err := os.Symlink("../usr/lib/os-release", filepath.Join(root, "etc/os-release")); err != nil {
		return err
	}
	// Mount points a read-only root cannot make at boot: /boot is where systemd's gpt-auto mounts
	// the ESP of the disk the machine booted from, which systemd-bless-boot and updates write.
	for _, d := range []string{"var", "tmp", "home", "boot", "efi"} {
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
		" root=/dev/dm-0 rootfstype=erofs ro init=/usr/lib/spindrift/init" +
		" console=ttyS0,115200n8 console=tty0 cgroup_no_v1=all,named" +
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
	shown, err := splash(splashLine{"Spindrift", 4}, splashLine{strings.TrimPrefix(label, "spindrift_"), 2})
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
func repart(stage, disk, label string, v verity, release string) error {
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
		// The release, for installing and updating; FAT, so any machine mounts it, and named so
		// spindrift-update finds it.
		// The release's files at its root: systemd-sysupdate reads SHA256SUMS from the URL it is
		// given, file:///run/spindrift/update, where it is mounted.
		"30-update.conf": fmt.Sprintf("[Partition]\nType=linux-generic\nLabel=spin-update\nFormat=vfat\nCopyFiles=%s:/\nSizeMinBytes=%d\n",
			abs(release), dirSize(release)*11/10+64<<20),
	} {
		if err := os.WriteFile(filepath.Join(defs, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return run("systemd-repart", "--empty=create", "--size=auto", "--dry-run=no", "--no-pager",
		"--definitions="+defs, "--seed="+v.rootUUID, disk)
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
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
