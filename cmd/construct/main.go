// Command construct is what the desktop's own root runs besides systemd-sysupdate, which writes
// its updates (usr/lib/sysupdate.d): installing it on a disk, checking a boot, the person's keys,
// the machine's channel and reports. usage, below, is the list of its commands and flags, and
// what construct with no arguments prints.
//
// install's release is the construct-update partition of the stick it runs from, checked against
// the public key the root carries (internal/update's KeyringPath) before anything is written.
package main

import (
	"github.com/Construct-Linux/os/internal/program"

	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/Construct-Linux/os/internal/gpt"
	"github.com/Construct-Linux/os/internal/update"
)

const usage = `usage:
  construct install    [--disk DEV] [--yes] [--locale LANG] [--keyboard LAYOUT] [--hostname NAME]
                   [--user NAME --real-name NAME --password-file FILE|-]
                   [--security-key DEV ... --key-pin-file FILE] [--json]
  construct disks      [--json]
  construct boot-check
  construct pending    [--mark FILE]
  construct theme      [--themes DIR] [--home DIR] [--config DIR]
  construct live
  construct firmware   FILE
  construct power      [--seconds N] | suspend
  construct bench      [--rounds N] [--seconds N]
  construct kernel-profile [--minutes N] [-o FILE]
  construct report
  construct diagnose   [-o DIR] [--power-seconds N]
  construct channel    [CHANNEL]
  construct key        list [--json] | add --name NAME [--device DEV] [--user NAME] [--pin-file FILE|-] | remove --name NAME | lock-on-removal [on|off]
                   | removed (udev's, when a key is unplugged)
  construct 1password  install | update`

func main() {
	// SIGHUP too: a dropped SSH session must still let a command undo what it changed (bench
	// puts the machine's own settings back), which a process killed by the signal never does.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	// systemd-sleep runs its hooks by name: usr/lib/systemd/system-sleep/construct-power is this.
	if filepath.Base(os.Args[0]) == "construct-power" {
		if err := sleepHook(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "construct-power:", err)
			os.Exit(1)
		}
		return
	}
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
	case "disks":
		err = disksCmd(os.Args[2:], os.Stdout)
	case "pending":
		err = pendingCmd(os.Args[2:], os.Stdout)
	case "theme":
		err = themeCmd(os.Args[2:], os.Stdout)
	case "live":
		err = liveCmd("/etc", "/var")
	case "firmware":
		err = firmwareCmd(ctx, os.Args[2:], os.Stdout)
	case "power":
		err = powerCmd(ctx, os.Args[2:], os.Stdout)
	case "bench":
		err = benchCmd(ctx, os.Args[2:], os.Stdout)
	case "report":
		err = reportCmd(ctx, os.Args[2:], os.Stdout)
	case "diagnose":
		err = diagnoseCmd(ctx, os.Args[2:], os.Stdout)
	case "channel":
		err = channelCmd(os.Args[2:], os.Stdout, "/")
	case "1password":
		err = onePasswordCmd(ctx, os.Args[2:], os.Stdout)
	case "key":
		err = keyCmd(ctx, os.Args[2:], os.Stdin, os.Stdout)
	case "kernel-profile":
		err = kernelProfileCmd(ctx, os.Args[2:], os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "construct:", err)
		os.Exit(1)
	}
}

// mountFS mounts the fstype file system at dev, with opts, on a directory of its own under
// /run/construct, and answers it and how to unmount it.
func mountFS(ctx context.Context, fstype, opts, dev string) (string, func() error, error) {
	if err := os.MkdirAll("/run/construct", 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("/run/construct", fstype+"-")
	if err != nil {
		return "", nil, err
	}
	if err := program.Run(ctx, "mount", "-t", fstype, "-o", opts, dev, dir); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	return dir, func() error {
		if err := program.Run(context.Background(), "umount", dir); err != nil {
			return err
		}
		return os.Remove(dir)
	}, nil
}

// mountVFAT mounts the new ESP, root's alone.
func mountVFAT(ctx context.Context, dev string) (string, func() error, error) {
	return mountFS(ctx, "vfat", "umask=0077", dev)
}

// mountStick mounts the release the stick this runs from carries: its construct-update partition,
// read-only, as construct-update@ mounts it.
func mountStick(ctx context.Context, medium string) (string, func() error, error) {
	f, err := os.Open("/dev/" + medium)
	if err != nil {
		return "", nil, err
	}
	table, err := gpt.Read(f)
	_ = f.Close()
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", medium, err)
	}
	p, err := table.Named("construct-update")
	if err != nil {
		return "", nil, fmt.Errorf("%s carries no release: %w", medium, err)
	}
	dev, err := update.PartitionDevice("/sys", medium, p.Number)
	if err != nil {
		return "", nil, err
	}
	return mountFS(ctx, "vfat", "ro,nodev,nosuid,noexec", dev)
}

func installCmd(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fl := flag.NewFlagSet("install", flag.ExitOnError)
	diskFlag := fl.String("disk", "", "the disk to install on: /dev/nvme0n1, or a link under /dev/disk (default: the machine's first disk that is neither removable nor USB)")
	yes := fl.Bool("yes", false, "do not ask: erase the disk")
	user := fl.String("user", "", "the login of whom the machine is for (default: asked)")
	realName := fl.String("real-name", "", "their full name (default: asked, or the login)")
	passwordFile := fl.String("password-file", "", "a file holding their password, - for standard input (default: asked)")
	locale := fl.String("locale", "", "their language, a locale: es_AR.UTF-8 (default: $LANG)")
	keyboard := fl.String("keyboard", "", "their keyboard layout, as GNOME's input sources write it: es, us+intl (default: /etc/vconsole.conf's)")
	hostname := fl.String("hostname", "", "the computer's name, one label: ada-x9 (default: asked; with --yes, "+defaultHostname+")")
	var keyDevices stringList
	fl.Var(&keyDevices, "security-key", "a FIDO2 key's hidraw node that opens the disk too, with its PIN and a touch; twice for a backup key")
	keyPINFile := fl.String("key-pin-file", "", "the security keys' PINs, one a line in --security-key's order: a file, or - for the lines after the password on standard input (pkexec passes no other descriptor)")
	jsonOut := fl.Bool("json", false, "tell what is done as JSON lines on standard output, progress included: what the graphical installer reads")
	_ = fl.Parse(args)
	log, progress := journal(), func(string, int64, int64) {}
	if *jsonOut {
		log = slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{ReplaceAttr: dropTime}))
		progress = percents(log)
	}
	ask := bufio.NewReader(in)

	// The release is checked against the key the running root trusts releases by.
	keys, err := update.ReadKeyring("/" + update.KeyringPath)
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
	size, err := os.ReadFile(filepath.Join("/sys/block", disk.Name, "queue", "logical_block_size"))
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(size)); s != fmt.Sprint(gpt.Sector) {
		return fmt.Errorf("%s has sectors of %s bytes; its table is written in %d", disk.Path(), s, gpt.Sector)
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
	if who.lang, err = languageFrom(*locale, *keyboard); err != nil {
		return err
	}
	if who.keys, err = securityKeysFrom(keyDevices, *keyPINFile, ask); err != nil {
		return err
	}
	switch {
	case *hostname != "":
		who.hostname, err = validHostname(*hostname)
	case !*yes:
		who.hostname, err = askHostname(ask, out)
	}
	if err != nil {
		return err
	}
	// /var is bound to this machine: the live system's id is the firmware's, as the installed one's.
	if err := update.FirmwareID("/sys"); err != nil {
		return err
	}
	machineID, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return err
	}
	varUUID, err := update.VarUUID(string(machineID))
	if err != nil {
		return err
	}
	release, unmount, err := mountStick(ctx, medium)
	if err != nil {
		return err
	}
	defer func() { _ = unmount() }() // read-only
	// systemd-boot, signed for Secure Boot, and the keys to enroll: the ESP this booted from -
	// the stick's - which systemd mounts (gpt-auto) and bootctl finds.
	var espPath bytes.Buffer
	if err := (program.With{Stdout: &espPath}).Run(ctx, "bootctl", "--print-esp-path"); err != nil {
		return err
	}
	esp := strings.TrimSpace(espPath.String())
	dev, err := os.OpenFile(disk.Path(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = dev.Close() }() // every write is synced where it is made
	tpm := hasTPM2("/sys")
	creds, err := bootCredentials(ctx, who.lang, tpm)
	if err != nil {
		return err
	}
	var recovery string
	err = update.Install(ctx, update.InstallOptions{
		Keys: keys, Source: release, Disk: disk, Device: dev,
		// BLKRRPART, which every partitioning tool asks.
		Reread: func() error { return unix.IoctlSetInt(int(dev.Fd()), unix.BLKRRPART, 0) },
		Partition: func(n int) (string, error) {
			return waitPartition(ctx, disk.Name, n)
		},
		Run:      program.Run,
		MountESP: mountVFAT,
		MakeVar: func(ctx context.Context, partition string) (err error) {
			recovery, err = makeVar(ctx, log, partition, who, tpm)
			return err
		},
		VarUUID: varUUID, Progress: progress,
		BootFrom: esp, Credentials: creds, Vars: update.Vars(update.EFIVars), UUID: update.RandomUUID, Log: log,
	})
	if err != nil {
		return err
	}
	// The recovery key goes where the person reads it, never to the journal.
	if *jsonOut {
		log.Info("done", "disk", disk.Path(), "user", who.user.Name, "tpm", tpm, "recovery_key", recovery)
		return nil
	}
	fmt.Fprintf(out, "installed on %s for %s: remove the stick and reboot\n\n", disk.Path(), who.user.Name)
	fmt.Fprintf(out, "Recovery key, which opens the disk in place of the password if it is lost or\nforgotten - write it down and keep it away from the computer:\n\n    %s\n", recovery)
	return nil
}

// percents is a Progress that logs each whole percent of a file once, as "progress" records.
func percents(log *slog.Logger) func(file string, done, total int64) {
	last := map[string]int64{}
	return func(file string, done, total int64) {
		if total <= 0 {
			return
		}
		pct := done * 100 / total
		if p, ok := last[file]; ok && p == pct {
			return
		}
		last[file] = pct
		log.Info("progress", "file", file, "percent", pct)
	}
}

// disksCmd lists the disks construct install can install on - never the stick it runs from - and
// the one it picks by itself, as text or, for the graphical installer, JSON.
func disksCmd(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("disks", flag.ExitOnError)
	jsonOut := fl.Bool("json", false, "JSON: a list of {path, model, size, serial, removable, usb, default}")
	_ = fl.Parse(args)
	medium, err := update.MediumDisk("/sys")
	if err != nil {
		return err
	}
	disks, err := update.Disks("/sys", medium)
	if err != nil {
		return err
	}
	def, _ := update.Default(disks)
	type row struct {
		Path      string `json:"path"`
		Model     string `json:"model"`
		Size      int64  `json:"size"`
		Serial    string `json:"serial"`
		Removable bool   `json:"removable"`
		USB       bool   `json:"usb"`
		Default   bool   `json:"default"`
	}
	rows := []row{}
	for _, d := range disks {
		rows = append(rows, row{d.Path(), d.Model, d.Size, d.Serial, d.Removable, d.USB, d.Name == def.Name})
	}
	if *jsonOut {
		return json.NewEncoder(out).Encode(rows)
	}
	for _, d := range disks {
		fmt.Fprintln(out, d)
	}
	return nil
}

// confirm shows disk and what is on it, and asks for its name typed back: everything on it goes.
func confirm(in *bufio.Reader, out io.Writer, disk update.BlockDisk) error {
	fmt.Fprintf(out, "Construct is to be installed on\n\n  %s\n\n", disk)
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

// journal is a logger for a unit's output, which journald stamps and names.
func journal() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: dropTime}))
}

// dropTime leaves a record's time out: the journal stamps it, and a reader of JSON needs none.
func dropTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
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
	mark := fl.String("mark", "", "write the pending version here, or remove this file when there is none")
	if err := fl.Parse(args); err != nil {
		return err
	}
	booted, err := update.BootedVersion("/usr/lib/os-release")
	if err != nil {
		return fmt.Errorf("the running version: %w", err)
	}
	// systemd's gpt-auto mounts the booted ESP at /boot.
	entries, err := os.ReadDir("/boot/EFI/Linux")
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

// stringList is a flag given more than once.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }
