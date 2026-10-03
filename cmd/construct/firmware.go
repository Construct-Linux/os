package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Firmware a person brings for their own machine, which the image cannot carry: a laptop maker's
// build whose license does not let anyone redistribute it. The ThinkPad X9-15p's Integrated Sensor
// Hub (ambient light, rotation, presence) boots only Lenovo's build, which is in Lenovo's Windows
// sensor driver - unpacked on Windows, its installer refuses anywhere else - and not in
// linux-firmware. It goes on /var, in firmwareDir, which the kernel searches before
// /usr/lib/firmware (firmware_class.path on the kernel image's command line, cmd/image).

// firmwareDir is where firmware a person installs lives.
const firmwareDir = "/var/lib/construct/firmware"

// ishAsked is a firmware name the sensor hub driver looked for: it tries names from the most
// specific (the machine's vendor, family and SKU) to the generic one, and logs each it lacks.
var ishAsked = regexp.MustCompile(`intel/ish/ish_[a-z0-9]+_[0-9a-f]{8}_[0-9a-f_]+\.bin`)

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
	name, err := ishName(log.String())
	if err != nil {
		return err
	}
	dst := filepath.Join(firmwareDir, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := writeSynced(dst, fw); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s as %s: restart for the sensor hub to boot it\n", args[1], dst)
	return nil
}

// ishName is the firmware the sensor hub asked for in the kernel's log: the longest name, the
// one made for this machine.
func ishName(kernelLog string) (string, error) {
	seen := map[string]bool{}
	var names []string
	for _, m := range ishAsked.FindAllString(kernelLog, -1) {
		if !seen[m] {
			seen[m] = true
			names = append(names, m)
		}
	}
	if len(names) == 0 {
		return "", errors.New("the sensor hub asked for no machine-specific firmware this boot: it has what it needs, or this machine has none")
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return names[0], nil
}

// writeSynced writes b to path whole, synced, so a power cut leaves the old file or the new one.
func writeSynced(path string, b []byte) error {
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncParent(path)
}

// syncParent syncs the directory holding path, so its new name outlives a power cut too.
func syncParent(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
