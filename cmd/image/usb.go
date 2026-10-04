package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/Construct-Linux/os/internal/update"
)

// usb makes the release a machine boots from a USB stick: an ESP with systemd-boot and a unified
// kernel image (its systemd initrd finds the root by the root hash on its command line), a
// read-only EROFS root under dm-verity and its hash tree, signed with task usb; usb-disk lays them
// on a disk, where systemd-repart makes /var at first boot in the rest of the stick. It runs as root in
// build/tools' container: it extracts the root with its owners and runs the image's own programs
// in it.
func usb(args []string) error {
	fl := flag.NewFlagSet("usb", flag.ExitOnError)
	test := fl.Bool("test", false, "add image/test/rootfs, as task disk -- -test does")
	var extra appendFlag
	fl.Var(&extra, "append", "more of the kernel's command line, to diagnose a boot: one option each, repeatable (plymouth.debug)")
	broken := fl.Bool("broken", false, "add image/test/broken: a release whose GDM fails, for the lifecycle test's rollback")
	pcrPub := fl.String("pcr-public", "", "the PCR key's public half, PEM (pcrsig.go)")
	sbKeys := fl.String("secureboot", "", "the Secure Boot variables to enroll, PK.auth, KEK.auth and db.auth (secureboot.go)")
	release := fl.String("version", "", "the release's version, YYYYMMDD.N (image release-version); without it, the build's time")
	_ = fl.Parse(args)
	if *pcrPub == "" || *sbKeys == "" {
		return fmt.Errorf("usb: -pcr-public and -secureboot are required")
	}
	if *release != "" && !calver.MatchString(*release) {
		return fmt.Errorf("usb: -version %q is not YYYYMMDD.N", *release)
	}
	if *release != "" && os.Getenv("CONSTRUCT_UPDATE_URL") == "" {
		return fmt.Errorf("usb: a release (-version) checks for updates where CONSTRUCT_UPDATE_URL says, and it is not set")
	}

	stage := filepath.Join(out, "usb")
	root := filepath.Join(stage, "root")
	layers := rootLayers(*test)
	if *broken {
		layers = append(layers, dirLayer{dir: "image/test/broken"})
	}
	version, err := bakedRoot(stage, root, layers, *release)
	if err != nil {
		return err
	}
	v, err := verifiedRoot(root, filepath.Join(stage, "root.img"), filepath.Join(stage, "root.verity"), version)
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(*pcrPub)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, pcrPublic), pub, 0o644); err != nil {
		return err
	}
	esp := filepath.Join(stage, "esp")
	label := update.ImageID + "_" + version
	if err := espTree(root, esp, label, v, strings.Join(extra, " "), filepath.Join(stage, pcrPublic), *sbKeys); err != nil {
		return err
	}
	b, err := json.Marshal(built{Version: version, RootHash: v.rootHash})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, builtFile), b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s, root hash %s: sign its PCR policy (image sign-pcr), then image usb-join\n", label, v.rootHash)
	return nil
}

// built is what image usb leaves for the steps after it, in the stage's builtFile.
type built struct {
	Version  string `json:"version"`
	RootHash string `json:"root_hash"`
}

const builtFile = "built.json"

// usbJoin joins the PCR policy's signature, which the host made, to the last usb's kernel image,
// in the stage: still without its Secure Boot signature, which image usb-sign adds.
func usbJoin(args []string) error {
	fl := flag.NewFlagSet("usb-join", flag.ExitOnError)
	_ = fl.Parse(args)
	stage := filepath.Join(out, "usb")
	if _, err := os.Stat(filepath.Join(stage, pcrSignature)); err != nil {
		return fmt.Errorf("the PCR policy is not signed (image sign-pcr): %w", err)
	}
	return run("ukify", "build",
		"--join-pcrsig", filepath.Join(stage, unsignedUKI),
		"--pcrsig", "@"+filepath.Join(stage, pcrSignature),
		"--output", filepath.Join(stage, joinedUKI))
}

// joinedUKI is the kernel image in the stage once its PCR policy's signature is joined.
const joinedUKI = "uki.joined.efi"

// usbSign finishes the last usb's release on the host, with db's key from -keys: the kernel image
// and systemd-boot signed for Secure Boot, the kernel image put in the ESP, and the release
// written, its SHA256SUMS for gpg to sign before image usb-disk.
func usbSign(args []string) error {
	fl := flag.NewFlagSet("usb-sign", flag.ExitOnError)
	ref := fl.String("key", "", "Secure Boot db's key: a PEM file, or kms:<key>")
	cert := fl.String("cert", "", "db's certificate (image secureboot-keys)")
	_ = fl.Parse(args)
	key, dbCertificate, err := dbSigner(*ref, *cert)
	if err != nil {
		return err
	}
	stage := filepath.Join(out, "usb")
	raw, err := os.ReadFile(filepath.Join(stage, builtFile))
	if err != nil {
		return fmt.Errorf("run image usb first: %w", err)
	}
	var b built
	if err := json.Unmarshal(raw, &b); err != nil {
		return err
	}
	esp := filepath.Join(stage, "esp")
	label := update.ImageID + "_" + b.Version
	uki := filepath.Join(esp, "EFI", "Linux", label+".efi")
	if err := os.Rename(filepath.Join(stage, joinedUKI), uki); err != nil {
		return fmt.Errorf("the joined kernel image (image usb-join): %w", err)
	}
	for _, p := range []string{uki, filepath.Join(esp, "EFI/BOOT/BOOTX64.EFI"), filepath.Join(esp, "EFI/systemd/systemd-bootx64.efi")} {
		if err := signPE(p, key, dbCertificate); err != nil {
			return err
		}
	}
	v := verityOf(b.RootHash, filepath.Join(stage, "root.img"), filepath.Join(stage, "root.verity"))
	rel, err := writeRelease(b.Version, esp, label, v)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s; sign SHA256SUMS, then image usb-disk\n", rel, label)
	return nil
}

// releaseDir is where a version's release is written: what an update reads, and what the stick's
// construct-update partition carries.
func releaseDir(version string) string { return filepath.Join(out, "release", version) }

// writeRelease lays version's release: the root and its tree named with the partition UUIDs a
// slot takes for them (the kernel image's command line names those), the kernel image, and
// SHA256SUMS over them, which the host signs (SHA256SUMS.gpg, systemd-sysupdate's name) before the
// stick is made. The names and the sums are internal/update's, which reads the release back.
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
	// at most); systemd-import and construct install decompress them as they write.
	rootFile, verityFile, uki := update.Names(version, v.rootUUID, v.vUUID)
	files := map[string]string{
		rootFile:   v.rootImg,
		verityFile: v.treeImg,
		uki:        filepath.Join(esp, "EFI", "Linux", label+".efi"),
	}
	for name, src := range files {
		dst := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".xz") {
			if err := run("sh", "-c", "xz -T0 -6 -c "+shellQuote(src)+" > "+shellQuote(dst)); err != nil {
				return "", err
			}
		} else if err := os.Link(src, dst); err != nil {
			return "", err
		}
	}
	return dir, update.WriteSums(dir)
}

// usbDisk lays the stick's image from the last usb's stage once its release is signed: the ESP,
// the root and its tree, and construct-update, a FAT partition holding the release - what a machine
// installs from, and updates from when the stick is plugged into one already installed.
func usbDisk(args []string) error {
	fl := flag.NewFlagSet("usb-disk", flag.ExitOnError)
	_ = fl.Parse(args)
	// writeRelease keeps one release: usb's.
	entries, err := os.ReadDir(filepath.Join(out, "release"))
	if err != nil || len(entries) != 1 {
		return fmt.Errorf("want the one release in %s: run image usb first", filepath.Join(out, "release"))
	}
	version := entries[0].Name()
	rel := releaseDir(version)
	if _, err := os.Stat(filepath.Join(rel, update.SignatureFile)); err != nil {
		return fmt.Errorf("%s is not signed: %w", rel, err)
	}
	b, err := os.ReadFile(filepath.Join(rel, update.SumsFile))
	if err != nil {
		return err
	}
	sums, err := update.ParseSums(b)
	if err != nil {
		return err
	}
	r, err := update.Find(sums)
	if err != nil {
		return err
	}
	label := update.ImageID + "_" + version
	stage := filepath.Join(out, "usb")
	// The stick boots the uncompressed root and tree the build left in its stage; the release
	// names the partition UUIDs they take.
	v := verity{rootImg: filepath.Join(stage, "root.img"), treeImg: filepath.Join(stage, "root.verity"),
		rootUUID: r.Root.UUID, vUUID: r.Verity.UUID}
	disk := filepath.Join(stage, "disk.raw")
	_ = os.Remove(disk)
	if err := repart(stage, disk, label, v, rel); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", disk, label)
	return nil
}

// bakedRoot lays the root a disk is made of in root, under a fresh stage: the layers merged,
// unpacked, and baked. The USB disk and the ext4 test disk are made from the same tree, so what
// the harness boots is what a machine boots. It answers the version it is baked as.
func bakedRoot(stage, root string, layers []layer, version string) (string, error) {
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := plymouthTheme(); err != nil {
		return "", err
	}
	if err := writeTransfers(); err != nil {
		return "", err
	}
	// Built on the host by task tools: this container has no Go.
	if _, err := os.Stat(filepath.Join(cliDir, "usr/bin/construct")); err != nil {
		return "", fmt.Errorf("construct (task tools builds it): %w", err)
	}
	merged := filepath.Join(out, "root.tar")
	if err := merge(layers, merged); err != nil {
		return "", err
	}
	if err := run("tar", "--numeric-owner", "--xattrs", "-xpf", merged, "-C", root); err != nil {
		return "", err
	}
	// A release's version is the builder's (calver.go); any other build's is its time, which sorts
	// after the day's releases.
	if version == "" {
		version = time.Now().UTC().Format("20060102.150405")
	}
	if err := bake(root, version); err != nil {
		return "", err
	}
	if problems := refs(root); len(problems) > 0 {
		return "", fmt.Errorf("the root's configuration names what it does not have:\n  %s", strings.Join(problems, "\n  "))
	}
	return version, nil
}

// bake makes now what a boot would otherwise write into a root it can no longer write: the
// packages' triggers apko does not run, the users sysusers.d declares, the factory files tmpfiles.d
// makes, the hardware database, the units the presets enable; and fstab and os-release.
func bake(root, version string) error {
	// What a person or a program writes outside /var on a writable root lives in /var here, as on
	// Fedora's image-based desktops: the read-only root links there (image/rootfs's tmpfiles.d makes
	// the targets). Laid first: systemd-tmpfiles below would make /root/.ssh in a /root still a
	// directory. /usr/local stays the image's: Wolfi's gcc puts cc and c++ in its bin.
	for link, target := range map[string]string{
		"home": "var/home", "opt": "var/opt", "srv": "var/srv", "mnt": "var/mnt", "root": "var/roothome",
	} {
		p := filepath.Join(root, link)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%s holds what the packages put there: %w", p, err)
		}
		if err := os.Symlink(target, p); err != nil {
			return err
		}
	}
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
		// The units the image runs, enabled from its presets (usr/lib/systemd/*-preset): systemd
		// links each where its [Install] says, Alias= names included.
		{"systemctl", "--root=" + root, "preset-all"},
		{"systemctl", "--root=" + root, "--global", "preset-all"},
		{"systemctl", "--root=" + root, "set-default", "graphical.target"},
	} {
		if err := run(args[0], args[1:]...); err != nil {
			return err
		}
	}
	if err := rootLocked(root); err != nil {
		return err
	}
	// The image's account files as they are, which an install starts a person's /etc from
	// (internal/account): the /etc it writes into has none of its own yet.
	factory := filepath.Join(root, "usr/share/factory/etc")
	if err := os.MkdirAll(factory, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"passwd", "group", "shadow", "gshadow"} {
		if err := run("cp", "-a", filepath.Join(root, "etc", f), filepath.Join(factory, f)); err != nil {
			return err
		}
	}
	files := map[string]string{
		// systemd makes a machine id at every boot, from the firmware's (cmdline), and mounts it here.
		"etc/machine-id": "",
		// What ConditionNeedsUpdate=/etc units (ldconfig, hwdb, the journal's catalog) would do at
		// boot, bake has done: without this file they run at every first boot of an image.
		"etc/.updated": "",
		"etc/fstab": `# / is the initrd's: EROFS under dm-verity, read-only, found and checked by the root hash on
# the command line (cmd/image usb). The initrd mounts /var, from the root's own disk, and lays /etc
# over it before it switches to the root (image/initrd).
tmpfs /tmp tmpfs mode=1777,nosuid,nodev 0 0
`,
		"usr/lib/os-release": fmt.Sprintf(`NAME="Construct"
ID=construct
ID_LIKE=wolfi
PRETTY_NAME="Construct %[1]s"
VERSION_ID=%[1]s
IMAGE_ID=construct
IMAGE_VERSION=%[1]s
LOGO=construct-logo
DEFAULT_HOSTNAME=construct
HOME_URL="https://github.com/Construct-Linux/os"
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
	// Mount points a read-only root cannot make at boot: /boot is where systemd's gpt-auto mounts
	// the ESP of the disk the machine booted from, which systemd-bless-boot and updates write.
	for _, d := range []string{"var", "tmp", "boot", "efi"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	return os.Chmod(filepath.Join(root, "tmp"), os.ModeSticky|0o777)
}

// varSkeleton records the directories and links the packages put in /var, with their owners and
// modes, as tmpfiles.d lines systemd-tmpfiles makes at every boot: /var is a partition of its own,
// which hides what the image has there. Files are not
// carried; a package that needs one in /var makes it itself. A path another tmpfiles.d file
// already makes (systemd's var.conf: /var/log, /var/lib...) is left to it: systemd-tmpfiles takes
// the first line for a path and warns of every other.
func varSkeleton(root string) error {
	declared, err := tmpfilesPaths(filepath.Join(root, "usr/lib/tmpfiles.d"))
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Written by cmd/image usb: the /var the packages made, which the /var partition hides.\n")
	base := filepath.Join(root, "var")
	err = filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := "/" + strings.TrimPrefix(p, root+"/")
		if p == base || rel == "/var/tmp" || declared[rel] {
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

// tmpfilesPaths are the paths the tmpfiles.d files in dir make lines for.
func tmpfilesPaths(dir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && !strings.HasPrefix(fields[0], "#") {
				paths[fields[1]] = true
			}
		}
	}
	return paths, nil
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

// rootLocked fails unless root has no password in the root's /etc/shadow ("*", as Wolfi ships it):
// on a read-only root nothing could take one away later, and sudo is how the person becomes root.
func rootLocked(root string) error {
	b, err := os.ReadFile(filepath.Join(root, "etc/shadow"))
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, ":"); len(f) > 2 && f[0] == "root" {
			if f[1] != "*" {
				return fmt.Errorf("root's password in /etc/shadow is %q, not *", f[1])
			}
			return nil
		}
	}
	return fmt.Errorf("root has no line in /etc/shadow")
}

// verity is what the kernel needs to check the root.
type verity struct {
	rootHash         string
	rootUUID, vUUID  string
	rootImg, treeImg string
}

const verityBlock = 4096

var rootHashLine = regexp.MustCompile(`(?m)^Root hash:\s+([0-9a-f]{64})$`)

// verifiedRoot writes root as an EROFS image, and its dm-verity tree to a file of its own for a
// partition of its own; the salt is the image's SHA-256, so the same tree is the same root.
func verifiedRoot(root, img, tree, version string) (verity, error) {
	// Every file's time is the release's: the same tree is the same image, and the time a cache
	// keys on changes with each release. Mesa's shader cache keys on its drivers' times where a
	// library has no build ID (Wolfi's libLLVM, which AMD's radeonsi hashes), and a time of 0 turns
	// it off - every launch compiled every shader again.
	built, err := time.Parse("20060102.150405", version)
	if err != nil {
		return verity{}, fmt.Errorf("version %q: %w", version, err)
	}
	if err := run("mkfs.erofs", "-zlz4hc", fmt.Sprintf("-T%d", built.Unix()), "--ignore-mtime", img, root); err != nil {
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
	outb, err := exec.Command("veritysetup", "format", img, tree, "--hash=sha256",
		fmt.Sprintf("--data-block-size=%d", verityBlock), fmt.Sprintf("--hash-block-size=%d", verityBlock),
		fmt.Sprintf("--data-blocks=%d", blocks), "--salt="+salt).CombinedOutput()
	if err != nil {
		return verity{}, fmt.Errorf("veritysetup: %w: %s", err, outb)
	}
	m := rootHashLine.FindSubmatch(outb)
	if m == nil {
		return verity{}, fmt.Errorf("veritysetup gave no root hash: %s", outb)
	}
	return verityOf(string(m[1]), img, tree), nil
}

// verityOf is the root of hash rh in img, its tree in tree: the partitions' UUIDs halve the hash.
func verityOf(rh, img, tree string) verity {
	uuid := func(h string) string {
		return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	}
	return verity{rootHash: rh, rootUUID: uuid(rh), vUUID: uuid(rh[32:]), rootImg: img, treeImg: tree}
}

// cmdline is the kernel's command line, in the kernel image where nothing at boot can change it.
// Plymouth draws on the firmware's framebuffer (simpledrm) from the start: the GPU's own driver
// is a module udev loads seconds later, and without plymouth.use-simpledrm Plymouth waits for it
// and the boot shows text instead. And with a serial console on the command line Plymouth runs as
// text on every console and looks for no screen at all, unless told to leave serial ones out.
//
// The root is systemd's to find: roothash= is all the initrd needs - the dissect_image udev builtin
// picks, on the disk the machine booted from, the root and verity partitions whose UUIDs halve the
// hash (slot A's or slot B's), and systemd-veritysetup checks one against the other, reading the
// rest from the tree's superblock. A block that does not match panics: panic=10 then reboots, and
// boot counting falls back. Then the consoles, the machine's id from its firmware (/var's UUID is
// derived from it), cgroup v2 only, firmware a person installed for their own machine searched
// first (construct firmware: /var, root's to write), and extra, options added for one build to
// diagnose a boot.
func (v verity) cmdline(extra string) string {
	return "roothash=" + v.rootHash + " systemd.verity_root_options=panic-on-corruption rootfstype=erofs ro" +
		" console=ttyS0,115200n8 console=tty0 cgroup_no_v1=all,named" +
		" systemd.machine_id=firmware panic=10 quiet loglevel=3 splash plymouth.use-simpledrm plymouth.ignore-serial-consoles" +
		" firmware_class.path=/var/lib/construct/firmware" + strings.TrimRight(" "+extra, " ")
}

// espTree lays the ESP: systemd-boot at the removable-media path, which firmware boots from a
// stick with no boot entry, and the Secure Boot variables in sbKeys for it to enroll
// (secureboot.go). The kernel image - with the microcode both vendors' processors load before the
// kernel runs, the initrd systemd runs in before the root (buildInitrd), and pcrPub, the PCR key's
// public half - is left in the stage unsigned, with its PCR 11 policy's digests, for the host to
// sign (pcrsig.go); image usb-sign signs it and systemd-boot and puts it in the ESP.
func espTree(root, esp, label string, v verity, extra, pcrPub, sbKeys string) error {
	boot, err := os.ReadFile(filepath.Join(root, "usr/lib/systemd/boot/efi/systemd-bootx64.efi"))
	if err != nil {
		return fmt.Errorf("the image's systemd-boot: %w", err)
	}
	for p, b := range map[string][]byte{
		"EFI/BOOT/BOOTX64.EFI":            boot,
		"EFI/systemd/systemd-bootx64.efi": boot,
		// Enroll the keys by itself in a VM, and offer it in the menu on a machine whose
		// firmware is in setup mode.
		"loader/loader.conf": []byte("timeout 3\neditor no\nsecure-boot-enroll if-safe\n"),
	} {
		full := filepath.Join(esp, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return err
		}
	}
	// "auto": the one name systemd-boot enrolls from by itself (in a VM); on a machine it is the
	// menu's "Enroll Secure Boot keys: auto".
	keys := filepath.Join(esp, "loader", "keys", "auto")
	if err := os.MkdirAll(keys, 0o755); err != nil {
		return err
	}
	for _, name := range sbVariables {
		b, err := os.ReadFile(filepath.Join(sbKeys, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(keys, name), b, 0o644); err != nil {
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
	rel, err := initrdRelease(root)
	if err != nil {
		return err
	}
	initrd := filepath.Join(filepath.Dir(esp), "initrd.cpio.zst")
	if err := buildInitrd(rel, initrd); err != nil {
		return err
	}
	stage := filepath.Dir(esp)
	digests, err := os.Create(filepath.Join(stage, pcrDigests))
	if err != nil {
		return err
	}
	defer digests.Close()
	cmd := exec.Command("ukify", "build",
		"--linux", filepath.Join(out, "kernel", "bzImage"),
		// The microcode first: the kernel looks for it at the start of the initrds.
		"--initrd", ucode, "--initrd", initrd,
		"--cmdline", v.cmdline(extra),
		"--os-release", "@"+filepath.Join(root, "usr/lib/os-release"),
		"--uname", strings.TrimSpace(readFile(filepath.Join(out, "kernel", "release"))),
		// The key systemd-stub hands the initrd, and the policy's digests for the host to sign.
		"--pcrpkey", pcrPub, "--pcr-public-key", pcrPub, "--pcr-banks", "sha256",
		"--policy-digest", "--json", "short",
		"--output", filepath.Join(stage, unsignedUKI))
	cmd.Stdout, cmd.Stderr = digests, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ukify: %w", err)
	}
	return digests.Close()
}

// unsignedUKI is the kernel image in the stage before its PCR policy is signed.
const unsignedUKI = "uki.unsigned.efi"

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
		// construct-update finds it.
		// The release's files at its root: systemd-sysupdate reads SHA256SUMS from the URL it is
		// given, file:///run/construct/update, where it is mounted.
		"30-update.conf": fmt.Sprintf("[Partition]\nType=linux-generic\nLabel=construct-update\nFormat=vfat\nCopyFiles=%s:/\nSizeMinBytes=%d\n",
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

// appendFlag collects -append's options, one per flag: a value with spaces does not survive the
// quoting task usb passes it through.
type appendFlag []string

func (a *appendFlag) String() string { return strings.Join(*a, " ") }

func (a *appendFlag) Set(v string) error {
	*a = append(*a, v)
	return nil
}
