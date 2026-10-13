package securitykey

import (
	"os"
	"path/filepath"
	"testing"
)

// pam_u2f's file has a line an account, its credentials in the order they were added.
func TestMappingsOf(t *testing.T) {
	got := mappings([]Key{
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
	keys := []Key{{Name: "YubiKey", User: "ada", Slot: 2, U2F: "kh,pk,es256,+presence", Added: "2026-10-05"}}
	if err := Write(etc, keys); err != nil {
		t.Fatal(err)
	}
	got, err := Read(etc)
	if err != nil || len(got) != 1 || got[0] != keys[0] {
		t.Fatalf("%+v %v", got, err)
	}
	m, err := os.ReadFile(filepath.Join(etc, mappingsFile))
	if err != nil || string(m) != "ada:kh,pk,es256,+presence\n" {
		t.Errorf("%q %v", m, err)
	}
	if fi, err := os.Stat(filepath.Join(etc, mappingsFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("%v %v", fi.Mode(), err)
	}
	if none, err := Read(t.TempDir()); err != nil || none != nil {
		t.Errorf("nothing registered: %+v %v", none, err)
	}
}
