package main

import "testing"

// The sensor hub asks for its firmware from the most specific name to the generic one, and the
// kernel logs each it lacks: the longest is this machine's.
func TestTheSensorHubsFirmwareIsTheNameMadeForThisMachine(t *testing.T) {
	log := `intel_ish_ipc 0000:00:12.0: firmware: failed to load intel/ish/ish_ptl_53c4ffad_6a9af742_7e1d2a90.bin (-2)
intel_ish_ipc 0000:00:12.0: firmware: failed to load intel/ish/ish_ptl_53c4ffad_6a9af742.bin (-2)
intel_ish_ipc 0000:00:12.0: firmware: failed to load intel/ish/ish_ptl_53c4ffad.bin (-2)
intel_ish_ipc 0000:00:12.0: ISH loader: cmd 2 failed 10`
	name, err := ishName(log)
	if err != nil || name != "intel/ish/ish_ptl_53c4ffad_6a9af742_7e1d2a90.bin" {
		t.Errorf("%q, %v", name, err)
	}
	if _, err := ishName("intel_ish_ipc 0000:00:12.0: ISH firmware loaded"); err == nil {
		t.Error("a name from a log that asks for none")
	}
}
