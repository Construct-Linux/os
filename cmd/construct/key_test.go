package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pam_u2f's file has a line an account, its credentials in the order they were added.
func TestMappingsOf(t *testing.T) {
	got := mappingsOf([]registeredKey{
		{Name: "YubiKey", User: "ada", U2F: "a1"},
		{Name: "Backup", User: "ada", U2F: "a2"},
		{Name: "Theirs", User: "ben", U2F: "b1"},
		{Name: "Disk only", User: "ben"},
	})
	if want := "ada:a1:a2\nben:b1\n"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// The record and the mappings are written together, and read back as they were.
func TestKeysRoundTrip(t *testing.T) {
	etc := t.TempDir()
	keys := []registeredKey{{Name: "YubiKey", User: "ada", Slot: 2, U2F: "kh,pk,es256,+presence", Added: "2026-10-05"}}
	if err := writeKeys(etc, keys); err != nil {
		t.Fatal(err)
	}
	got, err := readKeys(etc)
	if err != nil || len(got) != 1 || got[0] != keys[0] {
		t.Fatalf("%+v %v", got, err)
	}
	m, err := os.ReadFile(filepath.Join(etc, u2fMappings))
	if err != nil || string(m) != "ada:kh,pk,es256,+presence\n" {
		t.Errorf("%q %v", m, err)
	}
	if fi, err := os.Stat(filepath.Join(etc, u2fMappings)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("%v %v", fi.Mode(), err)
	}
	if none, err := readKeys(t.TempDir()); err != nil || none != nil {
		t.Errorf("nothing registered: %+v %v", none, err)
	}
}

// The keyslots a systemd-fido2 token opens, and not the TPM's or the recovery key's.
func TestFIDO2Slots(t *testing.T) {
	header := `{"keyslots":{},"tokens":{
		"0":{"type":"systemd-tpm2","keyslots":["1"]},
		"1":{"type":"systemd-recovery","keyslots":["2"]},
		"2":{"type":"systemd-fido2","keyslots":["4"]},
		"3":{"type":"systemd-fido2","keyslots":["3"]}}}`
	slots, err := fido2SlotsOf([]byte(header))
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

// fido2-cred -V's id and PEM key become pam_u2f's line: the id, x||y, es256, +presence.
func TestU2FLine(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.StdEncoding.EncodeToString([]byte("credential-id"))
	out := id + "\n" + string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	line, err := u2fLine([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(line, ",")
	if len(parts) != 4 || parts[0] != id || parts[2] != "es256" || parts[3] != "+presence" {
		t.Fatalf("%q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	point, _ := key.PublicKey.Bytes()
	if err != nil || len(raw) != 64 || string(raw) != string(point[1:]) {
		t.Errorf("public key %x, want %x", raw, point[1:])
	}
	if _, err := u2fLine([]byte(id + "\n")); err == nil {
		t.Error("no public key passed")
	}
}
