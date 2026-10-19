package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The name the sensor hub asks for first is the CRC-32s of the machine's DMI vendor, family,
// product and SKU, in that order: the ThinkPad X9-15p's, worked out apart with zlib.crc32. A
// field the firmware leaves out drops the names that need it.
func TestTheSensorHubsFirmwareIsTheNameMadeForThisMachine(t *testing.T) {
	const x9 = "intel/ish/ish_ptl_53c4ffad_6a9af742_d4894bf7_126d57fb.bin"
	if got := ishName("ptl", "LENOVO", "ThinkPad X9-15p Gen 1", "21VV005DCL", "LENOVO_MT_21VV_BU_Think_FM_ThinkPad X9-15p Gen 1"); got != x9 {
		t.Errorf("ishName = %q, want %q", got, x9)
	}
	if got := ishName("ptl", "LENOVO", "", "21VV005DCL", ""); got != "intel/ish/ish_ptl_53c4ffad_d4894bf7.bin" {
		t.Errorf("without family or SKU: %q", got)
	}
	if got := ishName("ptl", "", "", "", ""); got != "intel/ish/ish_ptl.bin" {
		t.Errorf("without DMI: %q", got)
	}
}

// The generation is the loaded firmware's, generic or not; a boot that loaded none says so.
func TestSensorHubGeneration(t *testing.T) {
	for log, want := range map[string]string{
		"intel_ish_ipc 0000:00:12.0: ISH loader: load firmware: intel/ish/ish_ptl.bin":                                      "ptl",
		"intel_ish_ipc 0000:00:12.0: ISH loader: load firmware: intel/ish/ish_lnlm_53c4ffad_6a9af742_d4894bf7_126d57fb.bin": "lnlm",
	} {
		if got, err := ishGeneration(log); err != nil || got != want {
			t.Errorf("%q: %q, %v", log, got, err)
		}
	}
	if _, err := ishGeneration("intel_ish_ipc 0000:00:12.0: ISH firmware loaded"); err == nil {
		t.Error("a generation from a log that loaded none")
	}
}

// Lenovo's driver packages, unpacked: the X9-15p's four tunings and the DSP firmware take the
// names its amplifiers ask for, Lenovo's newest sensor hub image the hub's, and what is another
// machine's (a tuning for model 2356), Intel's generic hub image, or not firmware stays behind.
func TestBroughtFromLenovosDrivers(t *testing.T) {
	const ish = "intel/ish/ish_ptl_53c4ffad_6a9af742_d4894bf7_126d57fb.bin"
	files := []string{
		"Audio/CS/XU_Ext/lenovo/tn/35L57/2355/dflt/b2_dflt_SS1_2355_LW_l1u0.bin",
		"Audio/CS/XU_Ext/lenovo/tn/35L57/2355/dflt/b2_dflt_SS1_2355_RW_l1u1.bin",
		"Audio/CS/XU_Ext/lenovo/tn/35L57/2355/dflt/b2_dflt_SS1_2355_LT_l0u2.bin",
		"Audio/CS/XU_Ext/lenovo/tn/35L57/2355/dflt/b2_dflt_SS1_2355_RT_l0u3.bin",
		"Audio/CS/XU_Ext/lenovo/tn/35L57/2356/dflt/b2_dflt_SS1_2356_LW_l1u0.bin",
		"Audio/CS/fw/35L56/b2_dflt_35l56_4.5.9.wmfw",
		"Audio/CS/fw/35L56/b2_dflt_35l56_4.5.12.wmfw",
		"Audio/CS/setup.exe",
		"Sensor/IntelDriver/IshHeci/FWImage/ishS_SI_5.8.1.7779.bin",
		"Sensor/Lenovo/FwImage/0004/ishS_SI_5.8.1.912.bin",
		"Sensor/Lenovo/FwImage/0004/ishS_SI_5.8.1.7779.bin",
	}
	b, err := brought(files, "17aa2355", ish)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1-l1u0.bin": files[0],
		"cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1-l1u1.bin": files[1],
		"cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1-l0u2.bin": files[2],
		"cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1-l0u3.bin": files[3],
		"cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1.wmfw":     files[6],
		ish: files[10],
	}
	for dst, src := range want {
		if b.files[dst] != src {
			t.Errorf("%s: %q, want %q", dst, b.files[dst], src)
		}
	}
	if len(b.files) != len(want) || !b.hub || len(b.notes) != 0 {
		t.Errorf("files %v, hub %v, notes %v", b.files, b.hub, b.notes)
	}

	// Without a sensor hub nothing of its is taken; a tree without the audio package says so;
	// two tunings for one amplifier is a tree to sort out, not a choice to make.
	b, err = brought(files[8:], "17aa2355", "")
	if err != nil || len(b.files) != 0 || b.hub || len(b.notes) != 1 {
		t.Errorf("without a hub, the sensor package alone: %v %v %v %v", b.files, b.hub, b.notes, err)
	}
	if _, err := brought(append(files[:1:1], "Other/35L57/b2_dflt_SS1_2355_LW_l1u0.bin"), "17aa2355", ""); err == nil {
		t.Error("two tunings for l1u0 taken")
	}
	b, _ = brought(files[:4], "17aa2355", "")
	if len(b.files) != 4 || len(b.notes) != 1 {
		t.Errorf("tunings without a DSP firmware: %v %v", b.files, b.notes)
	}
}

func TestVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"", "1", true}, {"5.8.1.912", "5.8.1.7779", true}, {"5.8.1.7779", "5.8.1.912", false}, {"4.5.9", "4.5.12", true}, {"1.0", "1.0", false}} {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// The sound device's subsystem id is read from the first PCI multimedia device that has one.
func TestSoundSSID(t *testing.T) {
	sys := t.TempDir()
	for dev, files := range map[string]map[string]string{
		"0000:00:02.0": {"class": "0x030000\n", "subsystem_vendor": "0x17aa\n", "subsystem_device": "0x2355\n"},
		"0000:00:1f.3": {"class": "0x040380\n", "subsystem_vendor": "0x17aa\n", "subsystem_device": "0x2355\n"},
	} {
		for name, body := range files {
			writeFile(t, filepath.Join(sys, "bus/pci/devices", dev, name), []byte(body))
		}
	}
	if got, err := soundSSID(sys); err != nil || got != "17aa2355" {
		t.Errorf("%q, %v", got, err)
	}
	if _, err := soundSSID(t.TempDir()); err == nil {
		t.Error("an id without a sound device")
	}
}

// Install carries what the stick was given, and a report names it.
func TestCarryFirmware(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "lib/construct/firmware")
	writeFile(t, filepath.Join(src, "intel/ish/ish_ptl_53c4ffad.bin"), []byte("hub"))
	writeFile(t, filepath.Join(src, "cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1.wmfw"), []byte("dsp"))
	n, err := carryFirmware(src, dst)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "intel/ish/ish_ptl_53c4ffad.bin")); err != nil || string(b) != "hub" {
		t.Error(string(b), err)
	}
	if n, err := carryFirmware(filepath.Join(src, "nothing"), dst); err != nil || n != 0 {
		t.Error("nothing brought:", n, err)
	}
	root := t.TempDir()
	if got := ownFirmware(root); got != "" {
		t.Error(got)
	}
	if err := os.MkdirAll(filepath.Join(root, firmwareDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ownFirmware(root); got != "none" {
		t.Error(got)
	}
	if _, err := carryFirmware(src, filepath.Join(root, firmwareDir)); err != nil {
		t.Fatal(err)
	}
	want := "cirrus/cs35l57-b2-dsp1-misc-17aa2355-spkid1.wmfw, intel/ish/ish_ptl_53c4ffad.bin"
	if got := ownFirmware(root); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
