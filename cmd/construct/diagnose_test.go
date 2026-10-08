package main

import (
	"testing"

	"github.com/Construct-Linux/os/internal/power"
	"github.com/Construct-Linux/os/internal/securitykey"
)

// Slots and tokens in order, each token with the slots it opens.
func TestLUKSSummary(t *testing.T) {
	h := securitykey.Header{
		Keyslots: map[string]securitykey.Keyslot{"1": {Type: "luks2"}, "0": {Type: "luks2"}},
		Tokens: map[string]securitykey.Token{"0": {Type: "systemd-tpm2", Keyslots: []string{"1"}},
			"1": {Type: "systemd-fido2", Keyslots: []string{"2"}}},
	}
	b := luksSummary("/dev/nvme0n1p6", h)
	want := "/dev/nvme0n1p6\nkeyslot 0: luks2\nkeyslot 1: luks2\ntoken 0: systemd-tpm2, keyslots 1\ntoken 1: systemd-fido2, keyslots 2\n"
	if string(b) != want {
		t.Errorf("luksSummary =\n%s\nwant\n%s", b, want)
	}
}

// Health and cycles are said only where the kernel gave them.
func TestBatteryLine(t *testing.T) {
	for b, want := range map[power.Battery]string{
		{Name: "BAT0", Status: "Discharging", Capacity: 81, Health: 90, Cycles: 212}: "BAT0: Discharging, 81%, health 90% of its design, 212 cycles",
		{Name: "BAT1", Status: "Full", Capacity: 100}:                                "BAT1: Full, 100%",
	} {
		if got := batteryLine(b); got != want {
			t.Errorf("batteryLine(%+v) = %q, want %q", b, got, want)
		}
	}
}
