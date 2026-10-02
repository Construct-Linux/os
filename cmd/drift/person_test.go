package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openwall/yescrypt-go"
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

// A password is hashed as libxcrypt hashes a new one - yescrypt at its default cost, a fresh salt
// each time - and the hash takes that password and no other.
func TestAPasswordIsHashedWithYescrypt(t *testing.T) {
	h, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, hashSetting) || len(strings.Split(h, "$")) != 5 {
		t.Fatalf("hash %q is not yescrypt's $y$j9T$<salt>$<hash>", h)
	}
	setting := h[:strings.LastIndex(h, "$")]
	for pw, want := range map[string]bool{"correct horse battery staple": true, "correct horse battery stapler": false} {
		again, err := yescrypt.Hash([]byte(pw), []byte(setting))
		if err != nil || (string(again) == h) != want {
			t.Errorf("%q against its hash: %s, %v; want a match %v", pw, again, err, want)
		}
	}
	if other, _ := hashPassword("correct horse battery staple"); other == h {
		t.Error("two hashes of a password share a salt")
	}
}

// crypt64 writes a salt as crypt(3) reads one: 16 bytes are 22 characters of its alphabet.
func TestCrypt64(t *testing.T) {
	if got := crypt64(make([]byte, 16)); got != strings.Repeat(".", 22) {
		t.Errorf("crypt64(16 zeros) = %q", got)
	}
	if got := crypt64([]byte{0xff, 0xff, 0xff}); got != "zzzz" {
		t.Errorf("crypt64(ff ff ff) = %q", got)
	}
}
