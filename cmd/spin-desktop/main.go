// Command spin-desktop is what the desktop's own root runs to change its version:
//
//	spin-desktop update     [--from DIR] [--esp DIR]            write a newer release into the other slot
//	spin-desktop boot-check                                     reboot a counted boot that did not come up
//	spin-desktop install    [--disk DEV] [--from DIR] [--yes]   lay the desktop on a machine's disk
//
// The release is a directory - an update stick's partition, mounted read-only - and is checked
// against the public key the root carries (internal/update's KeyringPath) before anything is
// written.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/spin-desktop/internal/gpt"
	"github.com/spin-stack/spin-desktop/internal/update"
)

const usage = `usage:
  spin-desktop update     [--from DIR] [--esp DIR] [--keyring FILE]
  spin-desktop boot-check
  spin-desktop install    [--disk DEV] [--from DIR] [--yes] [--keyring FILE]`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "update":
		err = updateCmd(ctx, os.Args[2:])
	case "boot-check":
		err = update.BootCheck(ctx, systemctl{}, update.Vars(update.EFIVars), journal())
	case "install":
		err = installCmd(ctx, os.Args[2:], os.Stdin, os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spin-desktop:", err)
		os.Exit(1)
	}
}

// defaultKeyring is the public key the running root trusts releases by.
var defaultKeyring = "/" + update.KeyringPath

func updateCmd(ctx context.Context, args []string) error {
	fl := flag.NewFlagSet("update", flag.ExitOnError)
	from := fl.String("from", update.DefaultSource, "the release, or a directory of releases: an update stick's partition, mounted")
	esp := fl.String("esp", "", "where the ESP the machine booted from is mounted (default: mount it, for this run)")
	keyring := fl.String("keyring", defaultKeyring, "the public key releases are checked against")
	_ = fl.Parse(args)

	keys, err := update.ReadKeyring(*keyring)
	if err != nil {
		return err
	}
	var root syscall.Stat_t
	if err := syscall.Stat("/", &root); err != nil {
		return err
	}
	espDir := *esp
	if espDir == "" {
		dir, unmount, err := mountBootedESP(ctx, "/sys", root.Dev)
		if err != nil {
			return err
		}
		defer func() { _ = unmount() }()
		espDir = dir
	}
	res, err := update.Run(ctx, update.Options{
		Keys: keys, OSRelease: "/usr/lib/os-release", Source: *from,
		Sys: "/sys", Root: root.Dev, OpenDisk: openDisk, ESP: espDir, Log: journal(),
	})
	if err != nil {
		return err
	}
	switch {
	case res.Written != "":
		fmt.Printf("wrote %s: the next boot starts it, and falls back to this one if it does not come up\n", res.Written)
	case res.Pending != "":
		fmt.Printf("%s is written and waiting: the next boot starts it\n", res.Pending)
	default:
		fmt.Println("nothing newer than this machine runs")
	}
	return nil
}

// mountBootedESP mounts, for one run, the ESP of the disk the running root is on - the desktop
// mounts none at boot - and answers where and how to unmount it.
func mountBootedESP(ctx context.Context, sys string, dev uint64) (string, func() error, error) {
	r, err := update.FindRoot(sys, dev)
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(r.Disk)
	if err != nil {
		return "", nil, err
	}
	table, err := gpt.Read(f)
	_ = f.Close()
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", r.Disk, err)
	}
	n := 0
	for i, p := range table.Partitions {
		if p.Type == gpt.TypeESP {
			n = i + 1
			break
		}
	}
	if n == 0 {
		return "", nil, fmt.Errorf("%s has no ESP", r.Disk)
	}
	part, err := update.PartitionDevice(sys, filepath.Base(r.Disk), n)
	if err != nil {
		return "", nil, err
	}
	return mountVFAT(ctx, part)
}

// mountVFAT mounts the FAT file system at dev under /run, and answers where and how to unmount it.
func mountVFAT(ctx context.Context, dev string) (string, func() error, error) {
	if err := os.MkdirAll("/run/spin-desktop", 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("/run/spin-desktop", "esp-")
	if err != nil {
		return "", nil, err
	}
	if err := run(ctx, "mount", "-t", "vfat", "-o", "umask=0077", dev, dir); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	return dir, func() error {
		unix.Sync()
		if err := run(context.Background(), "umount", dir); err != nil {
			return err
		}
		return os.Remove(dir)
	}, nil
}

func installCmd(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fl := flag.NewFlagSet("install", flag.ExitOnError)
	diskFlag := fl.String("disk", "", "the disk to install on: /dev/nvme0n1, or a link under /dev/disk (default: the machine's first disk that is neither removable nor USB)")
	from := fl.String("from", update.DefaultSource, "the release to install: an update stick's partition, mounted")
	yes := fl.Bool("yes", false, "do not ask: erase the disk")
	keyring := fl.String("keyring", defaultKeyring, "the public key the release is checked against")
	loader := fl.String("loader", "/usr/lib/systemd/boot/efi/systemd-bootx64.efi", "the systemd-boot the ESP is given")
	_ = fl.Parse(args)

	keys, err := update.ReadKeyring(*keyring)
	if err != nil {
		return err
	}
	// Never the stick this runs from.
	medium, err := update.MediumDisk("/sys")
	if err != nil {
		return err
	}
	disks, err := update.Disks("/sys", medium)
	if err != nil {
		return err
	}
	var disk update.BlockDisk
	if *diskFlag != "" {
		disk, err = update.Resolve(*diskFlag, "/dev", disks)
	} else {
		disk, err = update.Default(disks)
	}
	if err != nil {
		return err
	}
	if size, err := os.ReadFile(filepath.Join("/sys/block", disk.Name, "queue", "logical_block_size")); err != nil || strings.TrimSpace(string(size)) != fmt.Sprint(gpt.Sector) {
		return fmt.Errorf("%s has sectors of %q bytes; its table is written in %d", disk.Path(), strings.TrimSpace(string(size)), gpt.Sector)
	}
	if !*yes {
		if err := confirm(in, out, disk); err != nil {
			return err
		}
	}
	_, err = update.Install(ctx, update.InstallOptions{
		Keys: keys, Source: *from, Disk: disk,
		Open:   openDisk,
		Reread: reread,
		Partition: func(n int) (string, error) {
			return waitPartition(ctx, disk.Name, n)
		},
		Run:      run,
		MountESP: mountVFAT,
		Loader:   *loader, Vars: update.Vars(update.EFIVars), UUID: update.RandomUUID, Log: journal(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installed on %s: remove the stick and reboot\n", disk.Path())
	return nil
}

// confirm shows disk and what is on it, and asks for its name typed back: everything on it goes.
func confirm(in io.Reader, out io.Writer, disk update.BlockDisk) error {
	fmt.Fprintf(out, "Spin Desktop is to be installed on\n\n  %s\n\n", disk)
	if f, err := os.Open(disk.Path()); err == nil {
		if t, err := gpt.Read(f); err == nil {
			fmt.Fprintln(out, "which holds now:")
			for _, p := range t.Partitions {
				fmt.Fprintf(out, "  %-36q %9s\n", p.Name, sizeOf(p.Size()))
			}
			fmt.Fprintln(out)
		}
		_ = f.Close()
	}
	fmt.Fprintf(out, "Everything on it is erased. Type %s to go on: ", disk.Name)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != disk.Name {
		return errors.New("not confirmed: nothing was written")
	}
	return nil
}

func sizeOf(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

// openDisk opens a disk to be read and written.
func openDisk(path string) (update.Disk, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}

// reread has the kernel read the disk's new table: BLKRRPART, which every partitioning tool asks.
func reread(d update.Disk) error {
	f, ok := d.(*os.File)
	if !ok {
		return nil
	}
	return unix.IoctlSetInt(int(f.Fd()), unix.BLKRRPART, 0)
}

// waitPartition is the device of partition n of disk once the kernel lists it and its node is
// there, which follows a reread by moments.
func waitPartition(ctx context.Context, disk string, n int) (string, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		dev, err := update.PartitionDevice("/sys", disk, n)
		if err == nil {
			if _, serr := os.Stat(dev); serr == nil {
				return dev, nil
			}
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("%s did not appear", dev)
			}
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// run runs a program, its output where the caller's goes, and says what failed.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// journal is a logger for a unit's output, which journald stamps and names.
func journal() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
}

// systemctl is the machine's systemd, as a boot check asks it.
type systemctl struct{}

func (systemctl) Active(ctx context.Context, unit string) (bool, error) {
	err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return false, nil
	}
	return err == nil, err
}

func (systemctl) Reboot(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "systemctl", "reboot").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl reboot: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
