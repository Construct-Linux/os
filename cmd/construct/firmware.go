package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Construct-Linux/os/internal/fsx"
)

// Firmware a person brings for their own machine, which the image cannot carry: a laptop maker's
// build whose license does not let anyone redistribute it. The ThinkPad X9-15p's Integrated Sensor
// Hub (ambient light, rotation, presence) boots only Lenovo's build, which is in Lenovo's Windows
// sensor driver - unpacked on Windows, its installer refuses anywhere else - and not in
// linux-firmware. It goes on /var, in firmwareDir, which the kernel searches before
// /usr/lib/firmware (firmware_class.path on the kernel image's command line, cmd/image).

// firmwareDir is where firmware a person installs lives.
const firmwareDir = "/var/lib/construct/firmware"

// ishLoaded is the kernel's line for the firmware the sensor hub driver loaded, which names the
// hub's generation (ptl): the generic ish_<gen>.bin when no machine-specific one was there.
var ishLoaded = regexp.MustCompile(`ISH loader: load firmware: intel/ish/ish_([a-z0-9]+)(?:_[0-9a-f_]+)?\.bin`)

// firmwareCmd installs firmware: construct firmware ish FILE.
func firmwareCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "ish" {
		return errors.New("usage: construct firmware ish ishS_SI_<version>.bin (from the laptop maker's Windows sensor driver)")
	}
	fw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	// The hub's images are hundreds of kilobytes; anything else is not one.
	if len(fw) < 64<<10 || len(fw) > 8<<20 {
		return fmt.Errorf("%s is %d bytes: not a sensor hub firmware image", args[1], len(fw))
	}
	var log bytes.Buffer
	if err := cmdOut(ctx, nil, nil, &log, "journalctl", "-k", "-b", "-o", "cat", "--no-pager"); err != nil {
		return err
	}
	gen, err := ishGeneration(log.String())
	if err != nil {
		return err
	}
	dmi := func(f string) string {
		b, _ := os.ReadFile(filepath.Join("/sys/class/dmi/id", f))
		return strings.TrimSuffix(string(b), "\n")
	}
	name := ishName(gen, dmi("sys_vendor"), dmi("product_family"), dmi("product_name"), dmi("product_sku"))
	dst := filepath.Join(firmwareDir, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := fsx.WriteFile(dst, fw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s as %s: restart for the sensor hub to boot it\n", args[1], dst)
	return nil
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
