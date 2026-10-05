package account

import (
	"strings"
	"testing"

	"github.com/openwall/yescrypt-go"
)

// A password is hashed as libxcrypt hashes a new one - yescrypt at its default cost, a fresh salt
// each time - and the hash takes that password and no other.
func TestAPasswordIsHashedWithYescrypt(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
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
	if other, _ := HashPassword("correct horse battery staple"); other == h {
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
