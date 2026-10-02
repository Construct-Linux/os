// Command image makes Spindrift's disks and boots them: apko's root filesystem, the kernel's signed
// modules, the firmware and image/rootfs merged into one tree - a later layer's file replacing an
// earlier one's - and baked; then QEMU boots it, a virtio GPU behind VNC and QMP to take
// screenshots with.
//
//	image disk  [-test]                         the baked root as an ext4 test disk, booted with the kernel given directly
//	image boot  [-shot path.ppm -after 90s]     boot it; with -shot, take a screenshot and stop
//	image test  [-only a,b]                     boot the -test disk and drive the session (cmd/image/test.go)
//	image usb   -pcr-public pem [-test] [-append opt]  the stick's verified root and kernel image, in build/tools as root
//	image sign-pcr -key pem                     on the host: sign the kernel image's PCR 11 policy (cmd/image/pcrsig.go)
//	image usb-release                           the kernel image with its signed policy, and the release, in build/tools
//	image usb-disk                              the stick's disk from the signed release (cmd/image/usb.go)
//	image lifecycle                             install, update and roll back in QEMU (cmd/image/lifecycle.go)
//	image run   [-usb] [-wait 30s] -- <script>   boot, run script as root over vsock, stop
//	image play  [-fresh] [-stick=false]          the stick in a window on the host, with a disk to install on
//	image forge                                 the forged sticks the lifecycle must refuse
//	image check-key <keyring>                   the release keyring holds Ed25519 signing keys only
//	image check-langs                           every package in the root comes with its translations
//	image check                                 every ELF's NEEDED libraries are in the root
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spin-stack/spindrift/internal/update"
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
	case "usb-release":
		err = usbRelease(os.Args[2:])
	case "usb-disk":
		err = usbDisk(os.Args[2:])
	case "lifecycle":
		err = lifecycle(os.Args[2:])
	case "run":
		err = runIn(os.Args[2:])
	case "forge":
		err = forge(os.Args[2:])
	case "check-key":
		err = checkKey(os.Args[2:])
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
