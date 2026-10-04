package sign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Construct-Linux/os/internal/update"
)

func testKey(t *testing.T) (*Key, []byte, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKey(priv, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	pubring, err := k.Export("Construct release <release@construct.invalid>")
	if err != nil {
		t.Fatal(err)
	}
	return k, pubring, priv
}

// What the key signs verifies with another OpenPGP implementation, and what it did not sign does
// not; the keyring passes the image's own release-key policy.
func TestDetachSign(t *testing.T) {
	k, pubring, _ := testKey(t)
	if _, err := update.ParseKeyring(pubring); err != nil {
		t.Fatalf("the image's keyring check: %v", err)
	}
	data := []byte("e3b0c442  construct_20261004.1.efi\n")
	sig, err := k.DetachSign(bytes.NewReader(data), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pubring, bytes.NewReader(data), sig); err != nil {
		t.Fatalf("a good signature: %v", err)
	}
	if err := Verify(pubring, bytes.NewReader(append(data, '!')), sig); err == nil {
		t.Error("a signature over other data verified")
	}
	_, other, _ := testKey(t)
	if err := Verify(other, bytes.NewReader(data), sig); err == nil {
		t.Error("another key's keyring verified it")
	}
}

// gpg, which systemd-pull verifies SHA256SUMS.gpg with, takes the key and the signature.
func TestDetachSignGPG(t *testing.T) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("no gpg")
	}
	k, pubring, _ := testKey(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(data, []byte("e3b0c442  construct_20261004.1.efi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := k.DetachSign(f, time.Now())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data+".gpg", sig, 0o644); err != nil {
		t.Fatal(err)
	}
	keyring := filepath.Join(dir, "release.pgp")
	if err := os.WriteFile(keyring, pubring, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(gpg, "--homedir", home, "--batch", "--import", keyring).CombinedOutput(); err != nil {
		t.Fatalf("gpg --import: %v: %s", err, out)
	}
	out, err := exec.Command(gpg, "--homedir", home, "--batch", "--verify", data+".gpg", data).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Good signature") {
		t.Fatalf("gpg --verify: %v: %s", err, out)
	}
}

// The key is found again in the keyring it was exported to, with the fingerprint signatures carry;
// a keyring without it is refused.
func TestReadKey(t *testing.T) {
	k, pubring, priv := testKey(t)
	again, err := ReadKey(priv, pubring)
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint() != k.Fingerprint() {
		t.Errorf("fingerprint %x, exported %x", again.Fingerprint(), k.Fingerprint())
	}
	_, other, _ := testKey(t)
	if _, err := ReadKey(priv, other); err == nil {
		t.Error("a keyring without the key: no error")
	}
}

// A multiprecision integer drops its leading zero bytes and counts its bits from the first one.
func TestMPI(t *testing.T) {
	for _, c := range []struct {
		in, want []byte
	}{
		{[]byte{0x40, 0x01}, []byte{0, 15, 0x40, 0x01}},
		{[]byte{0x00, 0x00, 0x81}, []byte{0, 8, 0x81}},
		{[]byte{0x01}, []byte{0, 1, 0x01}},
	} {
		if got := mpi(c.in); !bytes.Equal(got, c.want) {
			t.Errorf("mpi(%x) = %x, want %x", c.in, got, c.want)
		}
	}
}
