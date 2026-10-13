package securitykey

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The keyslots a systemd-fido2 token opens, and not the TPM's or the recovery key's.
func TestSlots(t *testing.T) {
	h := Header{Tokens: map[string]Token{
		"0": {Type: "systemd-tpm2", Keyslots: []string{"1"}},
		"1": {Type: "systemd-recovery", Keyslots: []string{"2"}},
		"2": {Type: "systemd-fido2", Keyslots: []string{"4"}},
		"3": {Type: "systemd-fido2", Keyslots: []string{"3"}},
	}}
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

// A security key's token naming a keyslot that is no number is an error, not a slot passed over:
// the slot a key is removed by must be the one it opens.
func TestATokenOfNoKeyslotNumberIsRefused(t *testing.T) {
	h := Header{Tokens: map[string]Token{"0": {Type: "systemd-fido2", Keyslots: []string{"one"}}}}
	if slots, err := h.FIDO2Slots(); err == nil {
		t.Errorf("slots %v", slots)
	}
}

// fakeCryptsetup puts first on PATH a cryptsetup whose luksDump --dump-json-metadata prints
// header, and which fails asked anything else.
func fakeCryptsetup(t *testing.T, header string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "header.json"), []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		`[ "$1" = luksDump ] && [ "$2" = --dump-json-metadata ] || exit 1` + "\n" +
		`exec cat "$(dirname "$0")/header.json"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "cryptsetup"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A header holds kinds only: what luksDump says of salts, digests and the TPM's sealed blob is not
// read, so nothing made from a Header can carry it.
func TestHeaderHoldsNoSecret(t *testing.T) {
	fakeCryptsetup(t, `{"keyslots":{"0":{"type":"luks2","kdf":{"salt":"c2FsdA=="}}},
		"tokens":{"0":{"type":"systemd-tpm2","keyslots":["0"],"tpm2-blob":"YmxvYg=="}},
		"digests":{"0":{"digest":"ZGlnZXN0"}}}`)
	h, err := ReadHeader(t.Context(), "/dev/var")
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

// What cryptsetup prints that is not JSON is an error, not an empty header with no slots.
func TestAHeaderThatIsNotJSONIsRefused(t *testing.T) {
	fakeCryptsetup(t, "Device /dev/var is not a valid LUKS device.\n")
	if h, err := ReadHeader(t.Context(), "/dev/var"); err == nil {
		t.Errorf("header = %+v", h)
	}
}
