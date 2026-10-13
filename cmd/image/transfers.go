package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// transfersDir holds the systemd-sysupdate transfers writeTransfers makes, laid into the root.
var transfersDir = filepath.Join(out, "sysupdate")

// noUpdates is where a build that is not a release checks for updates: nowhere (.invalid), since
// only a release is ever offered to machines.
const noUpdates = "https://updates.construct.invalid"

// writeTransfers makes a set of transfers per channel, and the stick's, which differ in their
// source alone: the first channel's are the default sysupdate component, each other channel's its
// own component (sysupdate.<channel>.d, which construct channel switches to), and the stick's read
// a stick's construct-update partition mounted at /run/construct/update - definitions
// construct-update@ names, not a component, which systemd-sysupdated would check while no stick is
// there. All are url-file sources: only those make systemd-sysupdate (through systemd-pull) check
// SHA256SUMS.gpg against import-pubring.pgp and each file against SHA256SUMS as it copies it - a
// regular-file source is taken unchecked. updates is where machines check for updates: the update
// CDN (infra updates.tf), each channel's index at <updates>/<channel>/, or noUpdates.
func writeTransfers(updates string) error {
	sets := map[string]string{"usr/lib/construct/sysupdate-stick": "file:///run/construct/update"}
	for i, ch := range channels {
		dir := "usr/lib/sysupdate." + ch + ".d"
		if i == 0 {
			dir = "usr/lib/sysupdate.d"
		}
		sets[dir] = updates + "/" + ch
	}
	for dir, url := range sets {
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
// partitions of the slot the machine did not boot - labelled construct_<version>, with the
// UUIDs their names carry (@u), which the kernel image's command line names - and the kernel image
// last, onto the ESP with three tries: systemd-boot starts it at the next boot and falls back to
// the version that wrote it if it never reaches boot-complete.target (construct boot-check).
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
MatchPattern=construct_@v
MatchPartitionType=%s
ReadOnly=yes
`, url, file, typ)
	}
	return map[string]string{
		"10-root-verity.transfer": partition("construct_@v_@u.verity.raw.xz", "root-verity"),
		"20-root.transfer":        partition("construct_@v_@u.root.raw.xz", "root"),
		"90-uki.transfer": header + fmt.Sprintf(`[Transfer]
ProtectVersion=%%A

[Source]
Type=url-file
Path=%s
MatchPattern=construct_@v.efi

[Target]
Type=regular-file
Path=/EFI/Linux
PathRelativeTo=esp
MatchPattern=construct_@v+@l-@d.efi construct_@v+@l.efi construct_@v.efi
Mode=0644
TriesLeft=3
TriesDone=0
InstancesMax=2
`, url),
	}
}
