package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// initrdTar is task initrd's apko build of image/initrd/apko.yaml: Wolfi's systemd and what it
// runs in the initrd.
var initrdTar = filepath.Join(out, "initrd.tar")

// initrdTrim is what the initrd carries from its packages and never reads: documentation,
// translations, headers and static libraries, hwdb's sources (udev in the initrd needs no
// hardware database), and the shell completions.
var initrdTrim = []string{
	"usr/share/doc", "usr/share/man", "usr/share/info", "usr/share/locale", "usr/share/i18n",
	"usr/share/bash-completion", "usr/share/zsh", "usr/share/fish", "usr/share/gtk-doc",
	"usr/include", "usr/lib/pkgconfig", "usr/share/pkgconfig", "usr/lib/udev/hwdb.d",
	"var/lib/db/sbom",
}

// varRepart is /var's definition for systemd-repart in the initrd, which makes it on a stick's
// first boot in what the stick has beyond the image. Its UUID is the machine's (HMAC of the machine
// id, as the Discoverable Partitions Specification asks; the drop-in gives repart the id), so the
// dissect_image udev builtin designates it, and only on the machine that made it.
const varRepart = `[Partition]
Type=var
Label=var
Format=ext4
`

// buildInitrd packs the initrd the kernel image carries, at dst: task initrd's tree, the units
// of image/initrd/rootfs, the boot splash, its users and groups, and initrd-release, by which
// systemd knows it runs in an initrd. A newc cpio compressed with zstd, which the kernel unpacks
// itself (CONFIG_RD_ZSTD).
func buildInitrd(osRelease, dst string) error {
	dst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	stage := filepath.Join(filepath.Dir(dst), "initrd")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := run("tar", "--numeric-owner", "-xpf", initrdTar, "-C", stage); err != nil {
		return fmt.Errorf("%w: build it with task initrd", err)
	}
	for _, dir := range []string{"image/initrd/rootfs", themeDir} {
		if err := run("cp", "-a", dir+"/.", stage); err != nil {
			return err
		}
	}
	for _, p := range initrdTrim {
		if err := os.RemoveAll(filepath.Join(stage, p)); err != nil {
			return err
		}
	}
	for p, body := range map[string]string{
		"etc/initrd-release":           osRelease,
		"usr/lib/repart.d/60-var.conf": varRepart,
	} {
		full := filepath.Join(stage, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	// The users and groups the initrd's udev rules and tmpfiles name (tss, systemd-journal, ...):
	// its packages declare them, and nothing in the initrd runs systemd-sysusers.
	if err := run("systemd-sysusers", "--root="+stage); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(stage, "init"))
	if err := os.Symlink("usr/lib/systemd/systemd", filepath.Join(stage, "init")); err != nil {
		return err
	}
	pack := "cd " + shellQuote(stage) + " && find . -mindepth 1 | LC_ALL=C sort | cpio --quiet -o -H newc -R 0:0 | zstd -q -T0 -19 > " + shellQuote(dst)
	if err := run("sh", "-c", pack); err != nil {
		return fmt.Errorf("packing the initrd: %w", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Printf("initrd: %d MB\n", fi.Size()>>20)
	return nil
}

// initrdRelease is the initrd's os-release: the root's, as systemd-stub and the journal show it.
func initrdRelease(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "usr/lib/os-release"))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n") + "\nVARIANT_ID=initrd\n", nil
}
