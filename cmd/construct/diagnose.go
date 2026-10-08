package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/diagnose"
	"github.com/Construct-Linux/os/internal/power"
	"github.com/Construct-Linux/os/internal/securitykey"
)

// diagnoseCmd gathers into one archive what tells what a machine's image lacks or got wrong -
// devices without a driver, firmware not found, units and logins that failed, where the boot and
// the battery go - and prints its summary. Run on a laptop before its final install, it lists what
// to fix in the next build. As root: the journal of every unit, lspci's capabilities and the
// disk's header are root's to read. The archive holds the machine's serial numbers, never a key,
// a password or the LUKS header itself (its keyslots and tokens, by kind).
func diagnoseCmd(ctx context.Context, args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	dir := fl.String("o", "", "where the archive goes (default: the home of the person who ran sudo)")
	seconds := fl.Int("power-seconds", 20, "how long to measure the machine's draw over (0: not at all)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("diagnose: run it with sudo: the journal, lspci's details and the disk's header are root's")
	}
	person := os.Getenv("SUDO_USER")
	if *dir == "" {
		*dir = "."
		if u, err := user.Lookup(person); person != "" && err == nil {
			*dir = u.HomeDir
		}
	}
	host, _ := os.Hostname()
	name := fmt.Sprintf("construct-diagnose-%s-%s", host, time.Now().UTC().Format("20060102-150405"))
	files := diagnose.Gather(ctx, diagnoseProbes(person, time.Duration(*seconds)*time.Second))
	summary := diagnoseSummary(files)
	files["summary.txt"] = []byte(summary)
	path := filepath.Join(*dir, name+".tar.gz")
	if err := diagnose.WriteArchive(path, name, files); err != nil {
		return err
	}
	// The person who ran sudo opens and sends it, not root.
	if uid, err := strconv.Atoi(os.Getenv("SUDO_UID")); err == nil {
		gid, _ := strconv.Atoi(os.Getenv("SUDO_GID"))
		_ = os.Chown(path, uid, gid)
	}
	fmt.Fprint(stdout, summary)
	fmt.Fprintf(stdout, "\nEverything is in %s\n", path)
	return nil
}

// diagnoseProbes are the archive's files. A program the image lacks leaves its file saying so.
func diagnoseProbes(person string, measure time.Duration) []diagnose.Probe {
	cmd := diagnose.Command
	ps := []diagnose.Probe{
		{File: "report.txt", Run: func(ctx context.Context) ([]byte, error) {
			var b bytes.Buffer
			facts, journal := gatherReport(ctx, "/", execRunner)
			writeReport(&b, facts, journal)
			return b.Bytes(), nil
		}},
		cmd("hardware/lspci.txt", "lspci", "-nnk"),
		cmd("hardware/lspci-vv.txt", "lspci", "-vv"),
		cmd("hardware/lsusb.txt", "lsusb"),
		cmd("hardware/lsusb-tree.txt", "lsusb", "-t"),
		cmd("hardware/dmidecode.txt", "dmidecode"),
		cmd("hardware/lsblk.txt", "lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,TRAN,RM"),
		cmd("hardware/libinput.txt", "libinput", "list-devices"),
		cmd("hardware/sensors.txt", "sensors"),
		cmd("hardware/rfkill.txt", "rfkill"),
		cmd("hardware/fwupd.txt", "fwupdmgr", "get-devices", "--no-unreported-check"),
		cmd("hardware/modules.txt", "cat", "/proc/modules"),
		cmd("hardware/sound.txt", "cat", "/proc/asound/cards"),
		{File: "hardware/displays.txt", Run: func(context.Context) ([]byte, error) { return diagnose.Displays("/"), nil }},
		cmd("boot/cmdline.txt", "cat", "/proc/cmdline"),
		cmd("boot/time.txt", "systemd-analyze", "time"),
		cmd("boot/blame.txt", "systemd-analyze", "blame", "--no-pager"),
		cmd("boot/critical-chain.txt", "systemd-analyze", "critical-chain", "--no-pager"),
		cmd("boot/plot.svg", "systemd-analyze", "plot"),
		cmd("boot/bootctl.txt", "bootctl", "status", "--no-pager"),
		cmd("journal/errors.txt", "journalctl", "-b", "-p", "err", "-q", "--no-pager"),
		cmd("journal/errors.json", "journalctl", "-b", "-p", "err", "-q", "-o", "json", "--no-pager"),
		cmd("journal/warnings.txt", "journalctl", "-b", "-p", "warning", "-q", "--no-pager"),
		cmd("journal/kernel.txt", "journalctl", "-b", "-k", "-q", "--no-pager"),
		cmd("journal/previous-boot-errors.txt", "journalctl", "-b", "-1", "-p", "err", "-q", "--no-pager"),
		cmd("journal/auth.json", "journalctl", "-b", "-q", "-o", "json", "--no-pager",
			"SYSLOG_FACILITY=4", "+", "SYSLOG_FACILITY=10", "+", "_COMM=fprintd", "+", "_COMM=gdm-session-wor"),
		cmd("system/failed.txt", "systemctl", "--failed", "--no-legend", "--plain", "--no-pager"),
		cmd("system/units.txt", "systemctl", "list-units", "--all", "--no-pager"),
		cmd("system/coredumps.txt", "coredumpctl", "list", "--no-pager"),
		cmd("system/security.txt", "systemd-analyze", "security", "--no-pager"),
		cmd("power/upower.txt", "upower", "-d"),
		cmd("power/profile.txt", "powerprofilesctl"),
		cmd("power/sleep.txt", "sh", "-c", "cat /sys/power/mem_sleep; grep -r . /sys/power/suspend_stats"),
		cmd("network/nmcli.txt", "nmcli", "device", "status"),
		cmd("network/resolved.txt", "resolvectl", "status", "--no-pager"),
		{File: "auth/keys.txt", Run: func(ctx context.Context) ([]byte, error) {
			var b bytes.Buffer
			err := keyList(ctx, nil, &b)
			return b.Bytes(), err
		}},
		{File: "auth/luks.txt", Run: luksKinds},
	}
	if person != "" {
		ps = append(ps, cmd("auth/fingerprints.txt", "fprintd-list", person))
	}
	if measure > 0 {
		ps = append(ps, diagnose.Probe{File: "power/draw.txt", Run: func(ctx context.Context) ([]byte, error) {
			a, b, err := power.Sysfs{Root: "/"}.Measure(ctx, measure)
			if err != nil {
				return nil, err
			}
			return []byte(describePower(a, b)), nil
		}})
	}
	return ps
}

// luksKinds is /var's LUKS2 header as kinds: which keyslots there are and which token opens which
// - the password's, the TPM's, each security key's.
func luksKinds(ctx context.Context) ([]byte, error) {
	part, err := securitykey.VarPartition(ctx)
	if err != nil || part == "" {
		return []byte("/var is not encrypted\n"), err
	}
	h, err := securitykey.ReadHeader(ctx, part)
	if err != nil {
		return nil, err
	}
	return luksSummary(part, h), nil
}

func luksSummary(part string, h securitykey.Header) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", part)
	for _, k := range slices.Sorted(maps.Keys(h.Keyslots)) {
		fmt.Fprintf(&b, "keyslot %s: %s\n", k, h.Keyslots[k].Type)
	}
	for _, k := range slices.Sorted(maps.Keys(h.Tokens)) {
		fmt.Fprintf(&b, "token %s: %s, keyslots %s\n", k, h.Tokens[k].Type, strings.Join(h.Tokens[k].Keyslots, ","))
	}
	return b.Bytes()
}

// diagnoseSummary is what to look at first, read from the archive's files and the batteries.
func diagnoseSummary(files map[string][]byte) string {
	var b strings.Builder
	section := func(title string, items []string, none string) {
		fmt.Fprintf(&b, "%s:\n", title)
		if len(items) == 0 {
			fmt.Fprintf(&b, "  %s\n", none)
		}
		for _, it := range items {
			fmt.Fprintf(&b, "  %s\n", it)
		}
		b.WriteString("\n")
	}
	section("PCI devices with no driver", diagnose.DriverlessPCI(string(files["hardware/lspci.txt"])), "none")
	section("Firmware the kernel did not find", diagnose.MissingFirmware(string(files["journal/kernel.txt"])), "none")
	section("Failed units", lines(files["system/failed.txt"]), "none")
	section("Errors this boot, by unit", diagnose.ErrorsByUnit(files["journal/errors.json"], 15), "none")
	section("Authentication failures", diagnose.AuthFailures(files["journal/auth.json"], 20), "none")
	// systemd-analyze time's line, then blame's ten slowest units.
	boot := lines(files["boot/time.txt"])
	boot = boot[:min(1, len(boot))]
	blame := lines(files["boot/blame.txt"])
	section("Boot", append(boot, blame[:min(10, len(blame))]...), "unknown")
	var bats []string
	for _, bat := range (power.Sysfs{Root: "/"}).Batteries() {
		bats = append(bats, batteryLine(bat))
	}
	section("Batteries", bats, "none")
	return b.String()
}

// batteryLine is a battery as the summary says it: its state, charge and, where the kernel says,
// its health and cycles.
func batteryLine(b power.Battery) string {
	line := fmt.Sprintf("%s: %s, %d%%", b.Name, b.Status, b.Capacity)
	if b.Health > 0 {
		line += fmt.Sprintf(", health %.0f%% of its design", b.Health)
	}
	if b.Cycles > 0 {
		line += fmt.Sprintf(", %d cycles", b.Cycles)
	}
	return line
}
