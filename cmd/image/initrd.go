package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// initrdTar is task initrd's apko build of image/initrd/apko.yaml: Wolfi's systemd and what it
// runs in the initrd.
var initrdTar = filepath.Join(out, "initrd.tar")

// initrdTrim is what the initrd carries from its packages and never reads: documentation,
// translations, headers and static libraries, hwdb's sources (udev in the initrd needs no
// hardware database), the shell completions, and tpm2-tss's FAPI library, which systemd does not
// load (it loads ESYS, RC and MU) and which alone links libcurl. Globs are filepath.Match's.
var initrdTrim = []string{
	"usr/share/doc", "usr/share/man", "usr/share/info", "usr/share/locale", "usr/share/i18n",
	"usr/share/bash-completion", "usr/share/zsh", "usr/share/fish", "usr/share/gtk-doc",
	"usr/include", "usr/lib/pkgconfig", "usr/share/pkgconfig", "usr/lib/udev/hwdb.d",
	"var/lib/db/sbom", "usr/lib/libtss2-fapi.so*",
}

// initrdDrop are packages the initrd's own pull in and it never runs: tpm2-tss's FAPI library
// brings libcurl, which brings Kerberos twice (krb5 and Heimdal, through OpenLDAP and SASL) and
// a second OpenSSL; systemd brings curl and quota-tools for units an initrd does not start.
// Their files go, by the initrd's apk database; checkELF then proves nothing left links them.
var initrdDrop = []string{
	"curl", "libcurl-openssl4", "libpsl", "krb5-libs", "krb5-conf", "heimdal-libs",
	"cyrus-sasl-heimdal-libs", "libldap-2.7", "quota-tools",
}

// dropPackages removes the files the apk database under stage lists for each of names.
func dropPackages(stage string, names []string) error {
	db, err := os.ReadFile(filepath.Join(stage, "usr/lib/apk/db/installed"))
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	for _, block := range strings.Split(string(db), "\n\n") {
		var pkg, dir string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "P:"):
				pkg = line[2:]
			case strings.HasPrefix(line, "F:"):
				dir = line[2:]
			case strings.HasPrefix(line, "R:") && drop[pkg]:
				if err := os.Remove(filepath.Join(stage, dir, line[2:])); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
	}
	return nil
}

// linkDuplicates makes every regular file under dir that has the same contents as an earlier one
// a hard link to it, which the newc cpio stores once: Wolfi ships systemd's private library in
// two packages (libsystemd-shared and systemd), 8 MB each.
func linkDuplicates(dir string) error {
	seen := map[[sha256.Size]byte]string{}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		first, ok := seen[sum]
		if !ok {
			seen[sum] = p
			return nil
		}
		fa, errA := os.Stat(first)
		fb, errB := os.Stat(p)
		if errA != nil || errB != nil || fa.Mode() != fb.Mode() {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		return os.Link(first, p)
	})
}

// buildInitrd packs the initrd the kernel image carries, at dst: task initrd's tree, the units
// and /var's repart definition of image/initrd/rootfs, the boot splash, its users and groups, and initrd-release, by which
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
		matches, err := filepath.Glob(filepath.Join(stage, p))
		if err != nil {
			return err
		}
		for _, m := range matches {
			if err := os.RemoveAll(m); err != nil {
				return err
			}
		}
	}
	if err := dropPackages(stage, initrdDrop); err != nil {
		return err
	}
	if err := checkELF(dirLayer{dir: stage}); err != nil {
		return fmt.Errorf("the initrd: %w", err)
	}
	if err := linkDuplicates(stage); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "etc/initrd-release"), []byte(osRelease), 0o644); err != nil {
		return err
	}
	// The initrd is Spindrift's too: wolfi-baselayout's etc/os-release said Wolfi, and a tool that
	// reads it there (systemd-factory-reset matching a request to this OS) took it for another.
	for _, p := range []string{"etc/os-release", "usr/lib/os-release"} {
		_ = os.Remove(filepath.Join(stage, p))
	}
	if err := os.Symlink("initrd-release", filepath.Join(stage, "etc/os-release")); err != nil {
		return err
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
