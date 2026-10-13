package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Construct-Linux/os/internal/sign"
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
	// A release checks for updates at the builder's CONSTRUCT_UPDATE_URL, the update CDN.
	updates := os.Getenv("CONSTRUCT_UPDATE_URL")
	if updates == "" {
		if *release != "" {
			return fmt.Errorf("usb: a release (-version) checks for updates where CONSTRUCT_UPDATE_URL says, and it is not set")
		}
		updates = noUpdates
	}

	stage := filepath.Join(out, "usb")
	root := filepath.Join(stage, "root")
	layers := rootLayers(*test)
	if *broken {
		layers = append(layers, dirLayer{dir: "image/test/broken"})
	}
	version, err := bakedRoot(stage, root, layers, *release, updates)
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
	if err := espTree(root, esp, label, v, *test || *broken, strings.Join(extra, " "), filepath.Join(stage, pcrPublic), *sbKeys); err != nil {
		return err
	}
	b, err := json.Marshal(built{Version: version, RootHash: v.rootHash, Test: *test || *broken, Release: *release != ""})
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
	// Test is whether the root carries the test layer (an unsafe shell, the harness's hooks) or
	// the broken release's: such a build is never signed with a release's keys (refuseKMS).
	Test bool `json:"test"`
	// Release is whether Version is a release's (-version), not the build's time: YYYYMMDD.HHMMSS
	// reads as YYYYMMDD.N, so the version alone cannot say.
	Release bool `json:"release"`
}

const builtFile = "built.json"

// usbJoin joins the PCR policy's signature, which the host made, to the last usb's kernel image,
// in the stage: still without its Secure Boot signature, which image usb-sign adds.
func usbJoin(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usb-join takes no arguments")
	}
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
	stage := filepath.Join(out, "usb")
	b, err := readBuilt(stage)
	if err != nil {
		return err
	}
	if err := refuseKMS(b, *ref); err != nil {
		return err
	}
	key, dbCertificate, err := dbSigner(*ref, *cert)
	if err != nil {
		return err
	}
	esp := filepath.Join(stage, "esp")
	label := update.ImageID + "_" + b.Version
	// Each is signed from its unsigned source into the ESP, the sources left as they were: a
	// signing that failed half way - KMS refusing the third - is run again as it was, where
	// signing in place left a binary signed already, which is refused.
	boot := filepath.Join(stage, "root", "usr/lib/systemd/boot/efi/systemd-bootx64.efi")
	for dst, src := range map[string]string{
		filepath.Join(esp, "EFI", "Linux", label+".efi"):      filepath.Join(stage, joinedUKI),
		filepath.Join(esp, "EFI/BOOT/BOOTX64.EFI"):            boot,
		filepath.Join(esp, "EFI/systemd/systemd-bootx64.efi"): boot,
	} {
		raw, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("%s (image usb-join for the kernel image): %w", src, err)
		}
		signed, err := sign.EFI(raw, key, dbCertificate)
		if err != nil {
			return fmt.Errorf("signing %s: %w", src, err)
		}
		if err := os.WriteFile(dst, signed, 0o644); err != nil {
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

// readBuilt is what the last image usb built in stage.
func readBuilt(stage string) (built, error) {
	var b built
	raw, err := os.ReadFile(filepath.Join(stage, builtFile))
	if err != nil {
		return b, fmt.Errorf("run image usb first: %w", err)
	}
	return b, json.Unmarshal(raw, &b)
}

// refuseKMS refuses a release's key (KMS) for a build with the test layer, or one without a
// release's version (image usb without -version): signed with it, either would be a release to
// every machine.
func refuseKMS(b built, ref string) error {
	if !sign.IsKMS(ref) {
		return nil
	}
	if b.Test {
		return fmt.Errorf("%s is a test build: it is never signed with a release's key (%s)", b.Version, ref)
	}
	if !b.Release {
		return fmt.Errorf("%s is a build's time, not a release's version (-version): it is never signed with a release's key (%s)", b.Version, ref)
	}
	return nil
}

// releaseDir is where a version's release is written: what an update reads, and what the stick's
// construct-update partition carries.
func releaseDir(version string) string { return filepath.Join(out, "release", version) }

// currentRelease is the version of the release image usb-sign last wrote: writeRelease keeps one,
// and more than one is a directory something else wrote, which no step should guess between.
func currentRelease() (string, error) {
	dir := filepath.Join(out, "release")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		return "", fmt.Errorf("want the one release in %s: run image usb-sign first", dir)
	}
	return entries[0].Name(), nil
}

// writeRelease lays version's release: the root and its tree named with the partition UUIDs a
// slot takes for them (the kernel image's command line names those), the kernel image, and
// SHA256SUMS over them, which the host signs (SHA256SUMS.gpg, systemd-sysupdate's name) before the
// stick is made. The names and the sums are internal/update's, which reads the release back.
func writeRelease(version, esp, label string, v verity) (string, error) {
	dir := releaseDir(version)
	// One release at a time: every reader takes it (task usb signs it, usb-disk lays it,
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
	for name, src := range map[string]string{rootFile: v.rootImg, verityFile: v.treeImg} {
		if err := pipeline("", filepath.Join(dir, name), nil, []string{"xz", "-T0", "-6", "-c", src}); err != nil {
			return "", err
		}
	}
	// The kernel image goes as it is: its transfer (transfers.go) writes it to the ESP whole.
	if err := os.Link(filepath.Join(esp, "EFI", "Linux", label+".efi"), filepath.Join(dir, uki)); err != nil {
		return "", err
	}
	return dir, update.WriteSums(dir)
}

// usbDisk lays the stick's image from the last usb's stage once its release is signed: the ESP,
// the root and its tree, and construct-update, a FAT partition holding the release - what a machine
// installs from, and updates from when the stick is plugged into one already installed.
func usbDisk(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usb-disk takes no arguments")
	}
	version, err := currentRelease()
	if err != nil {
		return err
	}
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
// the harness boots is what a machine boots. It answers the version it is baked as. updates is
// where its transfers check for updates (writeTransfers).
func bakedRoot(stage, root string, layers []layer, version, updates string) (string, error) {
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := plymouthTheme(); err != nil {
		return "", err
	}
	if err := writeTransfers(updates); err != nil {
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
	if err := fontDirTimes(root, version); err != nil {
		return err
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

// fontDirTimes gives the font directories the time every file of the EROFS will have: fontconfig
// keeps a directory's cache only while the directory's time is the one the cache was made at, and
// with the times of the packages' unpacking every app scanned all fonts again at its start and
// wrote a cache of its own - where it could (etc/fonts/conf.d/05-construct-cache.conf).
func fontDirTimes(root, version string) error {
	built, err := builtAt(version)
	if err != nil {
		return err
	}
	for _, top := range []string{"usr/share/fonts", "usr/local/share/fonts"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			return os.Chtimes(p, built, built)
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// verifiedRoot writes root as an EROFS image, and its dm-verity tree to a file of its own for a
// partition of its own; the salt is the image's SHA-256, so the same tree is the same root.
func verifiedRoot(root, img, tree, version string) (verity, error) {
	// Every file's time is the release's: the same tree is the same image, and the time a cache
	// keys on changes with each release. Mesa's shader cache keys on its drivers' times where a
	// library has no build ID (Wolfi's libLLVM, which AMD's radeonsi hashes), and a time of 0 turns
	// it off - every launch compiled every shader again.
	built, err := builtAt(version)
	if err != nil {
		return verity{}, err
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

// verityOf is the root of hash rh in img, its tree in tree, in the slot the hash names.
func verityOf(rh, img, tree string) verity {
	root, v := update.SlotUUIDs(rh)
	return verity{rootHash: rh, rootUUID: root, vUUID: v, rootImg: img, treeImg: tree}
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
//
// The serial console is a test build's alone, for the harness's serial.log: on the command line it
// makes systemd-getty-generator start an agetty on ttyS0, and on a PC whose ttyS0 is a node with no
// UART behind it agetty fails ("could not get terminal name: -22") and restarts every few seconds,
// for as long as the machine is up. tty0 stays last, /dev/console either way.
//
// Without splash the boot is text, for a person finding where one stops: no Plymouth, in the
// initrd or after, the kernel's messages and each unit's status as it starts.
func (v verity) cmdline(serial, splash bool, extra string) string {
	consoles := " console=tty0"
	if serial {
		consoles = " console=ttyS0,115200n8" + consoles
	}
	shown := " quiet loglevel=3 splash plymouth.use-simpledrm plymouth.ignore-serial-consoles"
	if !splash {
		shown = " plymouth.enable=0 rd.plymouth=0 systemd.show_status=1"
	}
	return "roothash=" + v.rootHash + " systemd.verity_root_options=panic-on-corruption rootfstype=erofs ro" +
		consoles + " cgroup_no_v1=all,named systemd.machine_id=firmware panic=10" + shown +
		" firmware_class.path=/var/lib/construct/firmware" + strings.TrimRight(" "+extra, " ")
}

// The kernel image's profiles, which systemd-boot offers as entries of their own: the boot as it
// is, first and the default, and the same boot without the splash. ukify measures each and gives
// the policies of both to sign (pcrsig.go), so /var opens from the TPM under either.
const (
	mainProfile     = "ID=main"
	noSplashProfile = "ID=no-splash\nTITLE=without the splash: the boot's messages"
	noSplashPE      = "no-splash.profile.efi"
)

// espTree lays the ESP: systemd-boot at the removable-media path, which firmware boots from a
// stick with no boot entry, and the Secure Boot variables in sbKeys for it to enroll
// (secureboot.go). The kernel image - with the microcode both vendors' processors load before the
// kernel runs, the initrd systemd runs in before the root (buildInitrd), and pcrPub, the PCR key's
// public half - is left in the stage unsigned, with its PCR 11 policy's digests, for the host to
// sign (pcrsig.go); image usb-sign signs it and systemd-boot and puts it in the ESP.
func espTree(root, esp, label string, v verity, serial bool, extra, pcrPub, sbKeys string) error {
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
	for _, name := range sign.SecureBootVariables {
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
	// uname: systemd-boot and the stub show it, and an empty one is a kernel image no one can name.
	uname, err := os.ReadFile(filepath.Join(out, "kernel", "release"))
	if err != nil {
		return fmt.Errorf("the kernel's release (task kernel): %w", err)
	}
	stage := filepath.Dir(esp)
	digests, err := os.Create(filepath.Join(stage, pcrDigests))
	if err != nil {
		return err
	}
	defer digests.Close()
	noSplash := filepath.Join(stage, noSplashPE)
	if err := run("ukify", "build", "--profile", noSplashProfile, "--cmdline", v.cmdline(serial, false, extra), "--output", noSplash); err != nil {
		return err
	}
	cmd := exec.Command("ukify", "build",
		"--linux", filepath.Join(out, "kernel", "bzImage"),
		// The microcode first: the kernel looks for it at the start of the initrds.
		"--initrd", ucode, "--initrd", initrd,
		"--cmdline", v.cmdline(serial, true, extra),
		"--profile", mainProfile, "--join-profile", noSplash,
		"--os-release", "@"+filepath.Join(root, "usr/lib/os-release"),
		"--uname", strings.TrimSpace(string(uname)),
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
	names, err := treeNames(work, "kernel")
	if err != nil {
		return "", err
	}
	return dst, pipeline(work, dst, names, []string{"cpio", "-o", "-H", "newc", "--quiet", "--reproducible"})
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

// pipeline runs cmds in dir, each one's output the next one's input - stdin the first's, dst the
// last's - and fails when any of them does: a shell's pipeline counts only its last program, and a
// cpio that failed half way left a truncated initrd that zstd compressed, exiting 0, into a kernel
// image that was then signed.
func pipeline(dir, dst string, stdin []byte, cmds ...[]string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	procs := make([]*exec.Cmd, len(cmds))
	var in io.Reader = bytes.NewReader(stdin)
	// Once the children hold the pipes, ours would keep a reader waiting for an end that never came.
	var closeAfterStart []*os.File
	closePipes := func() {
		for _, f := range closeAfterStart {
			_ = f.Close()
		}
	}
	for i, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir, cmd.Stdin, cmd.Stderr = dir, in, os.Stderr
		if i == len(cmds)-1 {
			cmd.Stdout = out
		} else {
			r, w, err := os.Pipe()
			if err != nil {
				closePipes()
				return err
			}
			cmd.Stdout, in = w, r
			closeAfterStart = append(closeAfterStart, r, w)
		}
		procs[i] = cmd
	}
	for i, cmd := range procs {
		if err := cmd.Start(); err != nil {
			// Those already started see their pipes end and stop: waited for, none is left running
			// on a release's files after pipeline has failed.
			closePipes()
			for _, started := range procs[:i] {
				_ = started.Wait()
			}
			return fmt.Errorf("%s: %w", cmd.Args[0], err)
		}
	}
	closePipes()
	var errs []error
	for _, cmd := range procs {
		if err := cmd.Wait(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cmd.Args[0], err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return out.Close()
}

// treeNames is what `find top | LC_ALL=C sort` lists from dir, a line a path, as cpio reads them:
// top itself and everything under it, in byte order, so the same tree is the same archive.
func treeNames(dir, top string) ([]byte, error) {
	var names []string
	err := filepath.WalkDir(filepath.Join(dir, top), func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if top == "." && rel != "." {
			rel = "./" + rel
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return []byte(strings.Join(names, "\n") + "\n"), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// output runs cmd and answers what it printed, trimmed, or its error with what it said on
// stderr: an exit status alone - git's 128, aws's 255 - does not say why.
func output(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args[:min(len(cmd.Args), 2)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// appendFlag collects -append's options, one per flag: a value with spaces does not survive the
// quoting task usb passes it through.
type appendFlag []string

func (a *appendFlag) String() string { return strings.Join(*a, " ") }

func (a *appendFlag) Set(v string) error {
	*a = append(*a, v)
	return nil
}
