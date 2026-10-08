package main

import "testing"

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
