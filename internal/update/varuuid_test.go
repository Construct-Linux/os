package update

import (
	"os"
	"path/filepath"
	"testing"
)

// The vector is systemd's own: systemd-id128 machine-id --app-specific=<var's type> --uuid, on a
// machine whose id is the one given.
func TestVarUUIDIsSystemds(t *testing.T) {
	got, err := VarUUID("2125b34c411146e6844da0b4c60315bd\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cc7a4ffd-7252-425e-8a6a-2142f38a9264"; got != want {
		t.Fatalf("VarUUID = %s, want %s", got, want)
	}
	if _, err := VarUUID("not hex"); err == nil {
		t.Fatal("a malformed machine id was taken")
	}
}

// A machine binds /var to its firmware's UUID only when the firmware has one of its own.
func TestFirmwareID(t *testing.T) {
	for uuid, ok := range map[string]bool{
		"5f1d2c3a-7b8e-4c6d-9a0b-1c2d3e4f5a6b\n": true,
		"00000000-0000-0000-0000-000000000000\n": false,
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF\n": false,
		"":                                       false,
	} {
		sys := t.TempDir()
		if uuid != "" {
			p := filepath.Join(sys, "class", "dmi", "id", "product_uuid")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(uuid), 0o444); err != nil {
				t.Fatal(err)
			}
		}
		if err := FirmwareID(sys); (err == nil) != ok {
			t.Errorf("FirmwareID(%q) = %v, want ok %v", uuid, err, ok)
		}
	}
}
