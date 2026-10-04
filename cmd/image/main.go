// Command image makes Construct's disks and boots them: apko's root filesystem, the kernel's signed
// modules, the firmware and image/rootfs merged into one tree - a later layer's file replacing an
// earlier one's - and baked; then QEMU boots it, a virtio GPU behind VNC and QMP to take
// screenshots with.
//
//	image disk  [-test]                         the baked root as an ext4 test disk, booted with the kernel given directly
//	image boot  [-shot path.ppm -after 90s]     boot it; with -shot, take a screenshot and stop
//	image test  [-only a,b]                     boot the -test disk and drive the session (cmd/image/test.go)
//	image usb   -pcr-public pem -secureboot dir [-test] [-append opt]  the stick's verified root and kernel image, in build/tools as root
//	image sign-pcr -key ref                     on the host: sign the kernel image's PCR 11 policy (cmd/image/pcrsig.go)
//	image usb-join                              the kernel image with its signed policy, in build/tools
//	image usb-sign -key ref -cert db.crt        on the host: Secure Boot signatures, and the release (cmd/image/secureboot.go)
//	image sign-release -key ref -pubring pgp    on the host: the release's SHA256SUMS.gpg (cmd/image/keys.go)
//	image secureboot-keys -dir dir -pk -kek -db  PK's, KEK's and db's certificates and variables, once
//	image release-key -key ref -o release.pgp   the release key's OpenPGP public key, once
//	image public-key -key ref -o pem            a key's public half
//	image packages-list -o packages.txt         the baked root's packages, a release asset
//	image release-version                       the next vYYYYMMDD.N, from the remote's tags (cmd/image/calver.go)
//	image release-notes -version v -packages f  a release's notes (cmd/image/notes.go)
//	image channel-index -version v -add|-withdraw  a channel's index with a release added or withdrawn (cmd/image/channel.go)
//	image packages -missing|-prune              the recipes packages/ lacks, in build order; superseded builds out (cmd/image/packages.go)
//	image cache -pull|-push                     the release builder's packages/, kernel, firmware and QEMU in S3 (cmd/image/cache.go)
//	image usb-disk                              the stick's disk from the signed release (cmd/image/usb.go)
//	image lifecycle                             install, update and roll back in QEMU (cmd/image/lifecycle.go)
//	image run   [-usb] [-wait 30s] -- <script>   boot, run script as root over vsock, stop
//	image play  [-fresh] [-stick=false]          the stick in a window on the host, with a disk to install on
//	image type-dev                              the type specimen and its rendering matrix, in dev-up's machine (cmd/image/typespec.go)
//	image forge                                 the forged sticks the lifecycle must refuse
//	image check-key <keyring>                   the release keyring holds Ed25519 signing keys only
//	image check-langs                           every package in the root comes with its translations
//	image check                                 every ELF's NEEDED libraries are in the root
//
// A key is a reference (internal/sign): a PEM file, or kms:<key> for one in AWS KMS.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Construct-Linux/os/internal/update"
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
	case "sign-pcr":
		err = signPCR(os.Args[2:])
	case "usb-join":
		err = usbJoin(os.Args[2:])
	case "usb-sign":
		err = usbSign(os.Args[2:])
	case "secureboot-keys":
		err = secureBootKeys(os.Args[2:])
	case "usb-disk":
		err = usbDisk(os.Args[2:])
	case "lifecycle":
		err = lifecycle(os.Args[2:])
	case "dev-up":
		err = devUp(os.Args[2:])
	case "dev-down":
		err = devDown(os.Args[2:])
	case "theme-dev":
		err = themeDev(os.Args[2:])
	case "type-dev":
		err = typeDev(os.Args[2:])
	case "needles-propose":
		err = needlesPropose(os.Args[2:])
	case "scan-report":
		err = scanReport(os.Args[2:])
	case "run":
		err = runIn(os.Args[2:])
	case "forge":
		err = forge(os.Args[2:])
	case "check-key":
		err = checkKey(os.Args[2:])
	case "release-key":
		err = releaseKey(os.Args[2:])
	case "sign-release":
		err = signRelease(os.Args[2:])
	case "public-key":
		err = publicKey(os.Args[2:])
	case "packages-list":
		err = packagesList(os.Args[2:])
	case "release-version":
		err = releaseVersion(os.Args[2:])
	case "release-notes":
		err = releaseNotes(os.Args[2:])
	case "channel-index":
		err = channelIndex(os.Args[2:])
	case "packages":
		err = packagesCmd(os.Args[2:])
	case "cache":
		err = cacheCmd(os.Args[2:])
	case "play":
		err = play(os.Args[2:])
	case "check-langs":
		err = checkLangs(filepath.Join(out, "rootfs.tar"))
	case "check":
		err = checkELF(tarLayer{file: filepath.Join(out, "root.tar")})
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "image:", err)
		os.Exit(1)
	}
}

// checkKey refuses a release keyring with a signing key that is not Ed25519: the machine checks
// releases with gpg (systemd-sysupdate), which would take any key in it.
func checkKey(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("check-key <keyring>")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	_, err = update.ParseKeyring(raw)
	return err
}
