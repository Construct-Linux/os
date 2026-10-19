package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Construct-Linux/os/internal/fsx"
	"github.com/Construct-Linux/os/internal/program"
)

// Firmware a person brings for their own machine, which the image cannot carry: a laptop maker's
// build whose license does not let anyone redistribute it. The ThinkPad X9-15p's Integrated Sensor
// Hub (ambient light, rotation, presence) boots only Lenovo's build, and its four CS35L57
// amplifiers play at full volume only with Lenovo's tuning for them and Cirrus's DSP firmware;
// all of it is in Lenovo's Windows driver packages (the sensor driver's under Lenovo/FwImage,
// the audio driver's under CS/XU_Ext) - unpacked on Windows, their installers refuse anywhere
// else - and none of it in linux-firmware, which carries Dell's tunings for the same amplifier.
// It goes on /var, in firmwareDir, which the kernel searches before /usr/lib/firmware
// (firmware_class.path on the kernel image's command line, cmd/image), and construct install
// carries it onto the disk it installs (carryFirmware), so what the stick was given the machine
// keeps.

// firmwareDir is where firmware a person installs lives.
const firmwareDir = "/var/lib/construct/firmware"

// ishLoaded is the kernel's line for the firmware the sensor hub driver loaded, which names the
// hub's generation (ptl): the generic ish_<gen>.bin when no machine-specific one was there.
var ishLoaded = regexp.MustCompile(`ISH loader: load firmware: intel/ish/ish_([a-z0-9]+)(?:_[0-9a-f_]+)?\.bin`)

// firmwareCmd installs what a laptop maker ships for Windows alone: construct firmware PATH,
// the sensor hub's image on its own, or the directory Lenovo's driver packages were unpacked to,
// from which it takes whatever this machine's drivers ask for.
func firmwareCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: construct firmware DIR|FILE: the directory the laptop maker's Windows drivers were unpacked to (Lenovo's C:\\DRIVERS), or the sensor hub's ishS_SI_<version>.bin alone")
	}
	info, err := os.Stat(args[0])
	if err != nil {
		return err
	}
	if !info.IsDir() {
		fw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		// The hub's images are hundreds of kilobytes; anything else is not one.
		if len(fw) < 64<<10 || len(fw) > 8<<20 {
			return fmt.Errorf("%s is %d bytes: not a sensor hub firmware image", args[0], len(fw))
		}
		name, err := sensorHubName(ctx)
		if err != nil {
			return err
		}
		dst := filepath.Join(firmwareDir, name)
		if err := fsx.WriteFile(dst, fw, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "installed %s as %s\n", args[0], dst)
		restartNote(out, true)
		return nil
	}
	var files []string
	err = filepath.WalkDir(args[0], func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(args[0], path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	// A machine without a sensor hub, or whose driver did not start, takes no hub firmware; the
	// rest is still looked for.
	ish, ishErr := sensorHubName(ctx)
	if ishErr != nil {
		fmt.Fprintf(out, "sensor hub: %v\n", ishErr)
	}
	ssid, err := soundSSID("/sys")
	if err != nil {
		fmt.Fprintf(out, "speakers: %v\n", err)
	}
	b, err := brought(files, ssid, ish)
	if err != nil {
		return err
	}
	for _, n := range b.notes {
		fmt.Fprintln(out, n)
	}
	if len(b.files) == 0 {
		return fmt.Errorf("%s holds nothing this machine's drivers ask for", args[0])
	}
	for _, dst := range slices.Sorted(maps.Keys(b.files)) {
		fw, err := os.ReadFile(filepath.Join(args[0], filepath.FromSlash(b.files[dst])))
		if err != nil {
			return err
		}
		if err := fsx.WriteFile(filepath.Join(firmwareDir, filepath.FromSlash(dst)), fw, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "installed %s  <-  %s\n", dst, b.files[dst])
	}
	restartNote(out, b.hub)
	return nil
}

// restartNote says what happens at the next boot: the drivers ask for firmware as they probe,
// and the hub checks an image's signature only when told to start it (ishtp/loader.c).
func restartNote(out io.Writer, hub bool) {
	fmt.Fprintln(out, "restart for the drivers to load it.")
	if hub {
		fmt.Fprintln(out, "If the kernel then logs \"ISH loader: cmd 2 failed 10\", the hub refused its image: it is signed for another machine, or it is Intel's generic one (IntelDriver/IshHeci) and not the maker's (Lenovo/FwImage).")
	}
}

// sensorHubName is the name the sensor hub driver asks for first on this machine: its generation
// from the firmware the kernel loaded this boot, and the machine's DMI fields.
func sensorHubName(ctx context.Context) (string, error) {
	var log bytes.Buffer
	if err := (program.With{Stdout: &log}).Run(ctx, "journalctl", "-k", "-b", "-o", "cat", "--no-pager"); err != nil {
		return "", err
	}
	gen, err := ishGeneration(log.String())
	if err != nil {
		return "", err
	}
	dmi := func(f string) string {
		b, _ := os.ReadFile(filepath.Join("/sys/class/dmi/id", f))
		return strings.TrimSuffix(string(b), "\n")
	}
	return ishName(gen, dmi("sys_vendor"), dmi("product_family"), dmi("product_name"), dmi("product_sku")), nil
}

// ishGeneration is the sensor hub's generation, from the firmware the kernel loaded for it.
func ishGeneration(kernelLog string) (string, error) {
	m := ishLoaded.FindStringSubmatch(kernelLog)
	if m == nil {
		return "", errors.New("the kernel loaded no sensor hub firmware this boot: this machine has no sensor hub, or its driver did not start")
	}
	return m[1], nil
}

// ishName is the first name the sensor hub driver asks for (drivers/hid/intel-ish-hid/ishtp/
// loader.c, request_ish_firmware): from the most specific - the CRC-32s of the machine's DMI
// vendor, family, product name and SKU - to the vendor's alone, each tried when the fields it
// needs are there, then the generic ish_<gen>.bin. The driver looks for them without logging the
// ones it lacks (firmware_request_nowarn), so the name is worked out as it does.
func ishName(gen, vendor, family, product, sku string) string {
	crc := func(s string) string { return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(s))) }
	for _, parts := range [][]string{
		{vendor, family, product, sku},
		{vendor, family, sku},
		{vendor, family, product},
		{vendor, family},
		{vendor, product, sku},
		{vendor, sku},
		{vendor, product},
		{vendor},
	} {
		if slices.Contains(parts, "") {
			continue
		}
		name := "intel/ish/ish_" + gen
		for _, p := range parts {
			name += "_" + crc(p)
		}
		return name + ".bin"
	}
	return "intel/ish/ish_" + gen + ".bin"
}

// soundSSID is the subsystem id of the machine's PCI sound device, vendor then device as the
// amplifier driver writes it in the firmware names it asks for (cs35l56.c, "%04x%04x-spkid%d",
// from snd_soc_card_get_pci_ssid): 17aa2355 on the ThinkPad X9-15p.
func soundSSID(sysRoot string) (string, error) {
	devs, _ := filepath.Glob(filepath.Join(sysRoot, "bus/pci/devices/*/class"))
	for _, class := range devs {
		c, err := os.ReadFile(class)
		// PCI class 04: multimedia; 0401 audio, 0403 HD Audio and the DSPs behind it.
		if err != nil || !strings.HasPrefix(string(c), "0x04") {
			continue
		}
		dev := filepath.Dir(class)
		v, verr := os.ReadFile(filepath.Join(dev, "subsystem_vendor"))
		d, derr := os.ReadFile(filepath.Join(dev, "subsystem_device"))
		if verr != nil || derr != nil {
			continue
		}
		vendor, device := strings.TrimSpace(string(v)), strings.TrimSpace(string(d))
		if len(vendor) != 6 || len(device) != 6 || vendor == "0x0000" {
			continue
		}
		return vendor[2:] + device[2:], nil
	}
	return "", errors.New("no PCI sound device with a subsystem id: the speakers' tuning is not looked for")
}

// broughtFirmware is what a driver tree holds for this machine: the files to install, keyed by
// their name under firmwareDir, with the tree's own path for each, and notes on what was not
// there.
type broughtFirmware struct {
	files map[string]string
	notes []string
	// hub is true when the sensor hub's image is among the files.
	hub bool
}

var (
	// Lenovo's sensor hub image, signed for the machine: ishS_SI_<version>.bin under a
	// Lenovo/FwImage directory. Intel's generic build is elsewhere (IntelDriver/IshHeci), and the
	// hub refuses to start it.
	ishImage = regexp.MustCompile(`(?i)^ishS_SI_([0-9.]+)\.bin$`)
	// A CS35L5x tuning, b<rev>_dflt_SS<speaker id>_<model>_<speaker>_l<link>u<unit>.bin under a
	// 35L5x directory: the model is the sound device's subsystem device id, the speaker id the
	// amplifiers' strapping, link and unit the amplifier's place on SoundWire, which the driver
	// uses as the file's suffix (cs35l56_set_fw_suffix).
	tuning = regexp.MustCompile(`(?i)^b([0-9a-f]+)_dflt_SS([0-9]+)_([0-9a-f]{4})_[0-9a-z]+_(l[0-9]+u[0-9]+)\.bin$`)
	// The DSP's firmware, one for the family: b<rev>_dflt_35l5x_<version>.wmfw.
	dspFirmware = regexp.MustCompile(`(?i)^b([0-9a-f]+)_dflt_35l5[0-9a-z]*_([0-9.]+)\.wmfw$`)
	partDir     = regexp.MustCompile(`(?i)^35L5[0-9a-z]+$`)
)

// brought picks from files, a driver tree's regular files as slash paths, what this machine asks
// for: its sensor hub's image under ish (none when empty), and for the sound device whose
// subsystem id is ssid the amplifiers' tunings and their DSP firmware, under the names the
// driver asks for: cirrus/cs35l57-b2-dsp1-misc-<ssid>-spkid<N>-l<L>u<U>.bin and
// cirrus/cs35l57-b2-dsp1-misc-<ssid>-spkid<N>.wmfw, the names of an amplifier running no
// firmware of its own yet (cs35l56.c dsp_work, firmware_missing), the state a machine boots in.
func brought(files []string, ssid, ish string) (broughtFirmware, error) {
	b := broughtFirmware{files: map[string]string{}}
	if len(ssid) != 8 {
		ssid = ""
	}
	var hub, hubVersion string
	var dsp, dspVersion, dspRev string
	speakerIDs := map[string]bool{}
	var part string
	for _, f := range files {
		base := f[strings.LastIndex(f, "/")+1:]
		dirs := strings.Split(f, "/")
		dirs = dirs[:len(dirs)-1]
		if m := ishImage.FindStringSubmatch(base); m != nil {
			if !hasDirs(dirs, "lenovo", "fwimage") || !versionLess(hubVersion, m[1]) {
				continue
			}
			hub, hubVersion = f, m[1]
			continue
		}
		if m := dspFirmware.FindStringSubmatch(base); m != nil {
			if versionLess(dspVersion, m[2]) {
				dsp, dspVersion, dspRev = f, m[2], strings.ToLower(m[1])
			}
			continue
		}
		m := tuning.FindStringSubmatch(base)
		if m == nil || ssid == "" || !strings.EqualFold(m[3], ssid[4:]) {
			continue
		}
		i := slices.IndexFunc(dirs, partDir.MatchString)
		if i < 0 {
			continue
		}
		p := "cs" + strings.ToLower(dirs[i])
		if part != "" && p != part {
			return b, fmt.Errorf("tunings for two amplifiers, %s and %s: %s", part, p, f)
		}
		part = p
		dst := fmt.Sprintf("cirrus/%s-b%s-dsp1-misc-%s-spkid%s-%s.bin", part, strings.ToLower(m[1]), ssid, m[2], strings.ToLower(m[4]))
		if other, ok := b.files[dst]; ok {
			return b, fmt.Errorf("two tunings for the same amplifier: %s and %s", other, f)
		}
		b.files[dst] = f
		speakerIDs[strings.ToLower(m[1])+" "+m[2]] = true
	}
	switch {
	case ish == "":
	case hub != "":
		b.files[ish] = hub
		b.hub = true
	default:
		b.notes = append(b.notes, "sensor hub: no ishS_SI_<version>.bin under a Lenovo/FwImage directory: the sensor driver package is not here")
	}
	switch {
	case ssid == "":
	case part == "":
		b.notes = append(b.notes, "speakers: no tuning for a sound device "+ssid+" (b2_dflt_SS<N>_"+ssid[4:]+"_*.bin): the audio driver package is not here, or is another machine's")
	case dsp == "":
		b.notes = append(b.notes, "speakers: tunings without their DSP firmware (b2_dflt_35l5x_<version>.wmfw): the amplifiers run their own")
	default:
		for id := range speakerIDs {
			rev, spk, _ := strings.Cut(id, " ")
			if rev != dspRev {
				continue
			}
			b.files[fmt.Sprintf("cirrus/%s-b%s-dsp1-misc-%s-spkid%s.wmfw", part, rev, ssid, spk)] = dsp
		}
	}
	return b, nil
}

// hasDirs is true when every name is a directory on the path, case apart.
func hasDirs(dirs []string, names ...string) bool {
	for _, n := range names {
		if !slices.ContainsFunc(dirs, func(d string) bool { return strings.EqualFold(d, n) }) {
			return false
		}
	}
	return true
}

// versionLess orders dotted versions by their numbers: 5.8.1.7779 after 5.8.1.912, and anything
// after nothing.
func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// carryFirmware copies the firmware brought to this system, under src, to dst, firmwareDir on
// the /var being installed, where the installed machine's kernel finds it; it reports how many
// files. None is not an error: most machines bring nothing.
func carryFirmware(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		n++
		return fsx.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return n, err
}

// ownFirmware is what the person brought, for a report: the files under root's firmwareDir,
// none when the directory is empty, nothing when it is not there to read.
func ownFirmware(root string) string {
	dir := filepath.Join(root, firmwareDir)
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(files) == 0 {
		return "none"
	}
	slices.Sort(files)
	return strings.Join(files, ", ")
}
