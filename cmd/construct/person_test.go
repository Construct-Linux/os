package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskPersonRetriesUntilItHasOne(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("Ada Lovelace\nRoot\n\nsecret\nsecreT\nsecret\nsecret\n"))
	p, err := askPerson(in, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if p.user.Name != "ada" || p.user.RealName != "Ada Lovelace" || p.password != "secret" {
		t.Fatalf("person: %+v", p)
	}
	if p.user.SudoNoPassword {
		t.Fatal("an installed person sudoes without a password")
	}
}

func TestPersonFromFlags(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := personFrom(nil, io.Discard, "grace", "", pw)
	if err != nil || p.user.Name != "grace" || p.user.RealName != "grace" || p.password != "hunter2" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := personFrom(nil, io.Discard, "grace", "", ""); err == nil {
		t.Fatal("--user without --password-file was taken")
	}
	if _, err := personFrom(nil, io.Discard, "root", "", pw); err == nil {
		t.Fatal("root was taken as a login")
	}
}

func TestSuggestLogin(t *testing.T) {
	for in, want := range map[string]string{"Ada Lovelace": "ada", "  Grace  Hopper": "grace", "Ñandú": "and", "123": "user", "": "user"} {
		if got := suggestLogin(in); got != want {
			t.Errorf("suggestLogin(%q) = %q, want %q", in, got, want)
		}
	}
}

// The installer is in the live user's applications and autostart, theirs.
func TestOfferInstaller(t *testing.T) {
	home := t.TempDir()
	entry := filepath.Join(t.TempDir(), "io.github.construct_linux.Installer.desktop")
	if err := os.WriteFile(entry, []byte("[Desktop Entry]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := offerInstaller(home, entry, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".local/share/applications", ".config/autostart"} {
		if b, err := os.ReadFile(filepath.Join(home, dir, "io.github.construct_linux.Installer.desktop")); err != nil || string(b) != "[Desktop Entry]\n" {
			t.Errorf("%s: %q %v", dir, b, err)
		}
	}
}
