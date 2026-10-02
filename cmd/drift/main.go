// Command drift is what the desktop's own root runs besides systemd-sysupdate, which writes
// its updates (usr/lib/sysupdate.d):
//
//	drift install    [--disk DEV] [--from DIR] [--yes]   lay the desktop on a machine's disk
//	drift boot-check                                     reboot a counted boot that did not come up
//	drift pending    [--esp DIR] [--mark FILE]           the version the next boot starts, if newer
//	drift gtk4       [--from DIR] [--to DIR]             the image's GTK 4 theme, copied for a user
//	drift live                                           the live user, on a root nobody is installed for
//
// install's release is a directory - a stick's spin-update partition, mounted read-only - checked
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

	"github.com/spin-stack/spindrift/internal/gpt"
	"github.com/spin-stack/spindrift/internal/update"
)

const usage = `usage:
  drift install    [--disk DEV] [--from DIR] [--yes] [--keyring FILE] [--loader FILE]
  drift boot-check
  drift pending    [--esp DIR] [--mark FILE]
  drift gtk4       [--from DIR] [--to DIR]
  drift live`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "boot-check":
		err = update.BootCheck(ctx, systemctl{}, update.Vars(update.EFIVars), journal())
	case "install":
		err = installCmd(ctx, os.Args[2:], os.Stdin, os.Stdout)
	case "pending":
		err = pendingCmd(os.Args[2:], os.Stdout)
	case "gtk4":
		err = gtk4Cmd(os.Args[2:], os.Stdout)
	case "live":
		err = liveCmd("/etc", "/var")
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		os.Exit(1)
	}
}

// defaultKeyring is the public key the running root trusts releases by.
var defaultKeyring = "/" + update.KeyringPath

// mountVFAT mounts the FAT file system at dev under /run, and answers where and how to unmount it.
func mountVFAT(ctx context.Context, dev string) (string, func() error, error) {
	if err := os.MkdirAll("/run/spindrift", 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("/run/spindrift", "esp-")
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
	user := fl.String("user", "", "the login of whom the machine is for (default: asked)")
	realName := fl.String("real-name", "", "their full name (default: asked, or the login)")
	passwordFile := fl.String("password-file", "", "a file holding their password (default: asked)")
	_ = fl.Parse(args)
	ask := bufio.NewReader(in)

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
		if err := confirm(ask, out, disk); err != nil {
			return err
		}
	} else if *user == "" {
		return errors.New("--yes asks nothing: give --user and --password-file")
	}
	// Everything is asked before anything is written: the long part runs unattended.
	who, err := personFrom(ask, out, *user, *realName, *passwordFile)
	if err != nil {
		return err
	}
	// /var is bound to this machine: the live system's id is the firmware's, as the installed one's.
	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return err
	}
	varUUID, err := update.VarUUID(string(machineID))
	if err != nil {
		return err
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
		PrepareVar: func(ctx context.Context, dev string) error {
			return prepareVar(ctx, dev, who)
		},
		VarUUID: varUUID,
		Loader:  *loader, Vars: update.Vars(update.EFIVars), UUID: update.RandomUUID, Log: journal(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installed on %s for %s: remove the stick and reboot\n", disk.Path(), who.user.Name)
	return nil
}

// confirm shows disk and what is on it, and asks for its name typed back: everything on it goes.
func confirm(in *bufio.Reader, out io.Writer, disk update.BlockDisk) error {
	fmt.Fprintf(out, "Spindrift is to be installed on\n\n  %s\n\n", disk)
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
	line, err := in.ReadString('\n')
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

// pendingCmd prints the version systemd-boot starts at the next boot when it is newer than the
// running one, and with --mark writes it to that file - which the session turns into "restart to
// update" - or removes the file when nothing waits. It reads the ESP, not the update's source: the
// answer must not need the network, or the stick the update came from.
func pendingCmd(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("pending", flag.ContinueOnError)
	esp := fl.String("esp", "/boot", "where the ESP is mounted (systemd's gpt-auto mounts the booted one at /boot)")
	mark := fl.String("mark", "", "write the pending version here, or remove this file when there is none")
	osRelease := fl.String("os-release", "/usr/lib/os-release", "the running root's os-release")
	if err := fl.Parse(args); err != nil {
		return err
	}
	booted, err := update.BootedVersion(*osRelease)
	if err != nil {
		return fmt.Errorf("the running version: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(*esp, "EFI", "Linux"))
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	v := update.Pending(names, booted)
	if *mark != "" {
		if v == "" {
			if err := os.Remove(*mark); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(*mark), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(*mark, []byte(v+"\n"), 0o644); err != nil {
				return err
			}
		}
	}
	if v != "" {
		fmt.Fprintln(out, v)
	}
	return nil
}
