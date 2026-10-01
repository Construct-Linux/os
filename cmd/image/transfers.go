package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// transfersDir holds the systemd-sysupdate transfers writeTransfers makes, laid into the root.
var transfersDir = filepath.Join(out, "sysupdate")

// updateURL is where releases are published (GitHub Releases' newest): the default component's
// source, which spin-desktop-update.timer checks every day. SPIN_DESKTOP_UPDATE_URL moves it.
func updateURL() string {
	if u := os.Getenv("SPIN_DESKTOP_UPDATE_URL"); u != "" {
		return u
	}
	return "https://github.com/spin-stack/spin-desktop/releases/latest/download"
}

// writeTransfers makes the transfers of two sysupdate components that differ in their source
// alone: the default one, from updateURL, and "stick", from a stick's spin-update partition
// mounted at /run/spin-desktop/update. Both are url-file sources: only those make
// systemd-sysupdate (through systemd-pull) check SHA256SUMS.gpg against import-pubring.pgp and each
// file against SHA256SUMS as it copies it - a regular-file source is taken unchecked.
func writeTransfers() error {
	for dir, url := range map[string]string{
		"usr/lib/sysupdate.d":       updateURL(),
		"usr/lib/sysupdate.stick.d": "file:///run/spin-desktop/update",
	} {
		full := filepath.Join(transfersDir, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		for name, body := range transfers(url) {
			if err := os.WriteFile(filepath.Join(full, name), []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// transfers are a release's three files from url: the root's tree and the root, into the
// partitions of the slot the machine did not boot - labelled spin-desktop_<version>, with the
// UUIDs their names carry (@u), which the kernel image's command line names - and the kernel image
// last, onto the ESP with three tries: systemd-boot starts it at the next boot and falls back to
// the version that wrote it if it never reaches boot-complete.target (spin-desktop boot-check).
func transfers(url string) map[string]string {
	header := "# Written by cmd/image (transfers.go): a release from " + url + ".\n"
	partition := func(file, typ string) string {
		return header + fmt.Sprintf(`[Transfer]
ProtectVersion=%%A

[Source]
Type=url-file
Path=%s
MatchPattern=%s

[Target]
Type=partition
Path=auto
MatchPattern=spin-desktop_@v
MatchPartitionType=%s
ReadOnly=yes
`, url, file, typ)
	}
	return map[string]string{
		"10-root-verity.transfer": partition("spin-desktop_@v_@u.verity.raw.xz", "root-verity"),
		"20-root.transfer":        partition("spin-desktop_@v_@u.root.raw.xz", "root"),
		"90-uki.transfer": header + fmt.Sprintf(`[Transfer]
ProtectVersion=%%A

[Source]
Type=url-file
Path=%s
MatchPattern=spin-desktop_@v.efi

[Target]
Type=regular-file
Path=/EFI/Linux
PathRelativeTo=esp
MatchPattern=spin-desktop_@v+@l-@d.efi spin-desktop_@v+@l.efi spin-desktop_@v.efi
Mode=0444
TriesLeft=3
TriesDone=0
InstancesMax=2
`, url),
	}
}
