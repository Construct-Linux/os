package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// transfersDir holds the systemd-sysupdate transfers writeTransfers makes, laid into the root.
var transfersDir = filepath.Join(out, "sysupdate")

// updateURL is where releases are published (GitHub Releases' newest): the default component's
// source, which spindrift-update.timer checks every day. SPINDRIFT_UPDATE_URL moves it.
func updateURL() string {
	if u := os.Getenv("SPINDRIFT_UPDATE_URL"); u != "" {
		return u
	}
	return "https://github.com/spin-stack/spindrift/releases/latest/download"
}

// writeTransfers makes the transfers of two sysupdate components that differ in their source
// alone: the default one, from updateURL, and "stick", from a stick's spin-update partition
// mounted at /run/spindrift/update. Both are url-file sources: only those make
// systemd-sysupdate (through systemd-pull) check SHA256SUMS.gpg against import-pubring.pgp and each
// file against SHA256SUMS as it copies it - a regular-file source is taken unchecked.
func writeTransfers() error {
	for dir, url := range map[string]string{
		"usr/lib/sysupdate.d":       updateURL(),
		"usr/lib/sysupdate.stick.d": "file:///run/spindrift/update",
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
// partitions of the slot the machine did not boot - labelled spindrift_<version>, with the
// UUIDs their names carry (@u), which the kernel image's command line names - and the kernel image
// last, onto the ESP with three tries: systemd-boot starts it at the next boot and falls back to
// the version that wrote it if it never reaches boot-complete.target (drift boot-check).
// The kernel image is written 0644: a mode without write is FAT's read-only attribute, and
// systemd-boot counts a try by renaming the file - it cannot rename a read-only one, and then
// counts nothing and never falls back.
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
MatchPattern=spindrift_@v
MatchPartitionType=%s
ReadOnly=yes
`, url, file, typ)
	}
	return map[string]string{
		"10-root-verity.transfer": partition("spindrift_@v_@u.verity.raw.xz", "root-verity"),
		"20-root.transfer":        partition("spindrift_@v_@u.root.raw.xz", "root"),
		"90-uki.transfer": header + fmt.Sprintf(`[Transfer]
ProtectVersion=%%A

[Source]
Type=url-file
Path=%s
MatchPattern=spindrift_@v.efi

[Target]
Type=regular-file
Path=/EFI/Linux
PathRelativeTo=esp
MatchPattern=spindrift_@v+@l-@d.efi spindrift_@v+@l.efi spindrift_@v.efi
Mode=0644
TriesLeft=3
TriesDone=0
InstancesMax=2
`, url),
	}
}
