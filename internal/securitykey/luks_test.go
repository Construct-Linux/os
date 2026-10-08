package securitykey

import (
	"encoding/json"
	"strings"
	"testing"
)

// The keyslots a systemd-fido2 token opens, and not the TPM's or the recovery key's.
func TestSlots(t *testing.T) {
	header := `{"keyslots":{},"tokens":{
		"0":{"type":"systemd-tpm2","keyslots":["1"]},
		"1":{"type":"systemd-recovery","keyslots":["2"]},
		"2":{"type":"systemd-fido2","keyslots":["4"]},
		"3":{"type":"systemd-fido2","keyslots":["3"]}}}`
	h, err := parseHeader([]byte(header))
	if err != nil {
		t.Fatal(err)
	}
	slots, err := h.FIDO2Slots()
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

// A header holds kinds only: what luksDump says of salts, digests and the TPM's sealed blob is not
// read, so nothing made from a Header can carry it.
func TestHeaderHoldsNoSecret(t *testing.T) {
	h, err := parseHeader([]byte(`{"keyslots":{"0":{"type":"luks2","kdf":{"salt":"c2FsdA=="}}},
		"tokens":{"0":{"type":"systemd-tpm2","keyslots":["0"],"tpm2-blob":"YmxvYg=="}},
		"digests":{"0":{"digest":"ZGlnZXN0"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"c2FsdA==", "YmxvYg==", "ZGlnZXN0"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the header kept %s: %s", secret, b)
		}
	}
	if h.Keyslots["0"].Type != "luks2" || h.Tokens["0"].Type != "systemd-tpm2" {
		t.Errorf("header = %+v", h)
	}
}
