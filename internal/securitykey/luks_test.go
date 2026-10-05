package securitykey

import (
	"testing"
)

// The keyslots a systemd-fido2 token opens, and not the TPM's or the recovery key's.
func TestSlots(t *testing.T) {
	header := `{"keyslots":{},"tokens":{
		"0":{"type":"systemd-tpm2","keyslots":["1"]},
		"1":{"type":"systemd-recovery","keyslots":["2"]},
		"2":{"type":"systemd-fido2","keyslots":["4"]},
		"3":{"type":"systemd-fido2","keyslots":["3"]}}}`
	slots, err := slotsOf([]byte(header))
	if err != nil || len(slots) != 2 || slots[0] != 3 || slots[1] != 4 {
		t.Errorf("%v %v", slots, err)
	}
	if s, err := newSlot([]int{3}, []int{3, 4}); err != nil || s != 4 {
		t.Errorf("new slot %d %v", s, err)
	}
	if _, err := newSlot([]int{3}, []int{3}); err == nil {
		t.Error("no new slot passed")
	}
}
