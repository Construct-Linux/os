package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/securitykey"
)

// probe is one file of the diagnosis: a program's output (standard error too: what it could not
// read is part of the answer), or a function's.
type probe struct {
	file string
	args []string
	fn   func(ctx context.Context) ([]byte, error)
}

// probeTimeout bounds each probe: fwupd or a sensor that hangs costs one file, not the archive.
const probeTimeout = time.Minute

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
		if person != "" {
			*dir = filepath.Join("/var/home", person)
		}
	}
	host, _ := os.Hostname()
	name := fmt.Sprintf("construct-diagnose-%s-%s", host, time.Now().UTC().Format("20060102-150405"))
	files := map[string][]byte{}
	for _, p := range diagnoseProbes(person, *seconds) {
		files[p.file] = runProbe(ctx, p)
	}
	summary := diagnoseSummary(files, "/")
	files["summary.txt"] = []byte(summary)
	path := filepath.Join(*dir, name+".tar.gz")
	if err := writeArchive(path, name, files); err != nil {
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
func diagnoseProbes(person string, powerSeconds int) []probe {
	cmd := func(file string, args ...string) probe { return probe{file: file, args: args} }
	ps := []probe{
		{file: "report.txt", fn: func(ctx context.Context) ([]byte, error) {
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
		{file: "hardware/displays.txt", fn: func(context.Context) ([]byte, error) { return displays("/"), nil }},
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
		{file: "auth/keys.txt", fn: func(ctx context.Context) ([]byte, error) {
			var b bytes.Buffer
			err := keyList(ctx, nil, &b)
			return b.Bytes(), err
		}},
		{file: "auth/luks.txt", fn: luksKinds},
	}
	if person != "" {
		ps = append(ps, cmd("auth/fingerprints.txt", "fprintd-list", person))
	}
	if powerSeconds > 0 {
		ps = append(ps, probe{file: "power/draw.txt", fn: func(context.Context) ([]byte, error) {
			var b bytes.Buffer
			err := powerCmd([]string{"--seconds", strconv.Itoa(powerSeconds)}, &b)
			return b.Bytes(), err
		}})
	}
	return ps
}

// runProbe answers what p printed, and why it stopped when it failed.
func runProbe(ctx context.Context, p probe) []byte {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var out []byte
	var err error
	if p.fn != nil {
		out, err = p.fn(ctx)
	} else {
		out, err = exec.CommandContext(ctx, p.args[0], p.args[1:]...).CombinedOutput()
	}
	if err != nil {
		out = append(out, fmt.Sprintf("\n[construct diagnose: %s]\n", err)...)
	}
	return out
}

// luksKinds is /var's LUKS2 header as kinds: which keyslots there are and which token opens which
// - the password's, the TPM's, each security key's - without a salt or digest of it.
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

// displays are the DRM connectors, connected or not, with their modes: a panel the GPU's driver
// does not see is not in Settings either.
func displays(root string) []byte {
	conns, _ := filepath.Glob(filepath.Join(root, "sys/class/drm/card*-*"))
	var b bytes.Buffer
	for _, c := range conns {
		modes := strings.Fields(fileText(filepath.Join(c, "modes")))
		fmt.Fprintf(&b, "%s: %s, %d modes %s\n", filepath.Base(c), firstLine(filepath.Join(c, "status")),
			len(modes), strings.Join(modes[:min(3, len(modes))], " "))
	}
	return b.Bytes()
}

func fileText(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// diagnoseSummary is what to look at first, read from the archive's files and the battery.
func diagnoseSummary(files map[string][]byte, root string) string {
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
	section("PCI devices with no driver", driverlessPCI(string(files["hardware/lspci.txt"])), "none")
	section("Firmware the kernel did not find", missingFirmware(string(files["journal/kernel.txt"])), "none")
	section("Failed units", lines(files["system/failed.txt"]), "none")
	section("Errors this boot, by unit", errorsByUnit(files["journal/errors.json"], 15), "none")
	section("Authentication failures", authFailures(files["journal/auth.json"], 20), "none")
	section("Boot", bootSummary(string(files["boot/time.txt"]), string(files["boot/blame.txt"]), 10), "unknown")
	section("Batteries", batteries(root), "none")
	return b.String()
}

// driverlessPCI are the devices lspci -nnk lists with no "Kernel driver in use" line: hardware
// the kernel has no driver for, or whose driver did not bind.
func driverlessPCI(lspci string) []string {
	var out []string
	var dev string
	bound := true
	flush := func() {
		if dev != "" && !bound {
			out = append(out, dev)
		}
	}
	for _, l := range strings.Split(lspci, "\n") {
		if l == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			flush()
			dev, bound = l, false
			continue
		}
		if strings.Contains(l, "Kernel driver in use:") {
			bound = true
		}
	}
	flush()
	return out
}

var firmwareFailed = regexp.MustCompile(`(?:Direct firmware load for (\S+) failed|firmware: failed to load (\S+))`)

// missingFirmware are the files the kernel asked for and did not find, each once.
func missingFirmware(kernel string) []string {
	var out []string
	for _, m := range firmwareFailed.FindAllStringSubmatch(kernel, -1) {
		f := m[1] + m[2]
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// journalEntry is what the summary reads of journalctl -o json's records.
type journalEntry struct {
	Unit       string `json:"_SYSTEMD_UNIT"`
	UserUnit   string `json:"_SYSTEMD_USER_UNIT"`
	Identifier string `json:"SYSLOG_IDENTIFIER"`
	Comm       string `json:"_COMM"`
	Message    any    `json:"MESSAGE"`
}

func (e journalEntry) source() string {
	for _, s := range []string{e.UserUnit, e.Unit, e.Identifier, e.Comm} {
		if s != "" {
			return s
		}
	}
	return "?"
}

// message is MESSAGE, which journalctl writes as an array of bytes when it is not UTF-8.
func (e journalEntry) message() string {
	switch m := e.Message.(type) {
	case string:
		return m
	case []any:
		var b []byte
		for _, c := range m {
			if f, ok := c.(float64); ok {
				b = append(b, byte(f))
			}
		}
		return string(b)
	}
	return ""
}

func journalEntries(b []byte) []journalEntry {
	var out []journalEntry
	for _, l := range lines(b) {
		var e journalEntry
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// errorsByUnit counts the boot's errors by where they came from, the most first, with the last
// message of each.
func errorsByUnit(b []byte, n int) []string {
	counts := map[string]int{}
	last := map[string]string{}
	for _, e := range journalEntries(b) {
		counts[e.source()]++
		last[e.source()] = e.message()
	}
	srcs := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	var out []string
	for _, s := range srcs[:min(n, len(srcs))] {
		out = append(out, fmt.Sprintf("%4d  %s: %s", counts[s], s, oneLineCut(last[s], 120)))
	}
	return out
}

var authFailed = regexp.MustCompile(`(?i)authentication failure|auth could not identify|verify-no-match|verify-disconnected|verify-unknown-error|failed to (?:claim|open|verify)|no match|incorrect password|conversation failed|timed out`)

// authFailures are the boot's failed logins, unlocks, sudo and polkit prompts, fingerprint and
// security key tries: the last n.
func authFailures(b []byte, n int) []string {
	var out []string
	for _, e := range journalEntries(b) {
		if m := e.message(); authFailed.MatchString(m) {
			out = append(out, e.source()+": "+oneLineCut(m, 140))
		}
	}
	return out[max(0, len(out)-n):]
}

// bootSummary is systemd-analyze time's line and blame's first n units.
func bootSummary(timeOut, blame string, n int) []string {
	out := lines([]byte(timeOut))[:min(1, len(lines([]byte(timeOut))))]
	bl := lines([]byte(blame))
	return append(out, bl[:min(n, len(bl))]...)
}

// batteries are each battery's state, charge and health - its full charge against its design's.
func batteries(root string) []string {
	dirs, _ := filepath.Glob(filepath.Join(root, "sys/class/power_supply/BAT*"))
	var out []string
	for _, d := range dirs {
		v := func(name string) string { return strings.TrimSpace(fileText(filepath.Join(d, name))) }
		line := fmt.Sprintf("%s: %s, %s%%", filepath.Base(d), v("status"), v("capacity"))
		full, design := v("energy_full"), v("energy_full_design")
		if full == "" {
			full, design = v("charge_full"), v("charge_full_design")
		}
		f, ferr := strconv.ParseFloat(full, 64)
		dz, derr := strconv.ParseFloat(design, 64)
		if ferr == nil && derr == nil && dz > 0 {
			line += fmt.Sprintf(", health %.0f%% of its design", 100*f/dz)
		}
		if c := v("cycle_count"); c != "" && c != "0" {
			line += ", " + c + " cycles"
		}
		out = append(out, line)
	}
	return out
}

// oneLineCut is s on one line, cut to n characters: a journal message in a person's language has
// letters of more than one byte, and a cut inside one is not text.
func oneLineCut(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// writeArchive writes files under dir in a gzipped tar at path, root's alone until chowned; an
// archive it could not finish is removed, not left half written.
func writeArchive(path, dir string, files map[string][]byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	now := time.Now()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + name, Mode: 0o644, Size: int64(len(body)), ModTime: now}); err != nil {
			f.Close()
			return err
		}
		if _, err := tw.Write(body); err != nil {
			f.Close()
			return err
		}
	}
	for _, c := range []io.Closer{tw, zw} {
		if err := c.Close(); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
