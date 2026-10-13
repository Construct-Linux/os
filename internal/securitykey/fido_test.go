package securitykey

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

// fido2-cred -V's id and PEM key become pam_u2f's line: the id, x||y, es256, +presence.
func TestCredentialLine(t *testing.T) {
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
	line, err := credentialLine([]byte(out))
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
	if _, err := credentialLine([]byte(id + "\n")); err == nil {
		t.Error("no public key passed")
	}
}

// What is no es256 credential never becomes a line of pam_u2f's mappings, which sudo, polkit and
// the lock screen take a touch by: no id, an id that is not base64, a key that is not ECDSA, or
// one on a curve other than P-256.
func TestWhatIsNoES256CredentialIsRefused(t *testing.T) {
	publicPEM := func(pub any) string {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := base64.StdEncoding.EncodeToString([]byte("credential-id"))
	for name, out := range map[string]string{
		"no id":            "\n" + publicPEM(&p256.PublicKey),
		"no line at all":   id,
		"an id not base64": "credential id!\n" + publicPEM(&p256.PublicKey),
		"a key of junk":    id + "\n" + string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("der")})),
		"an Ed25519 key":   id + "\n" + publicPEM(edPub),
		"a P-384 key":      id + "\n" + publicPEM(&p384.PublicKey),
	} {
		if line, err := credentialLine([]byte(out)); err == nil {
			t.Errorf("%s became %q", name, line)
		}
	}
}

// A new key says noclientPin among its options; one with a PIN says clientPin.
func TestNoClientPIN(t *testing.T) {
	fresh := "proto: 0x02\nmajor: 0x05\noptions: rk, up, noplat, noclientPin, credentialMgmtPreview\n"
	set := "options: rk, up, noplat, clientPin, credentialMgmtPreview\n"
	if !noClientPIN(fresh) || noClientPIN(set) || noClientPIN("aaguid: 00\n") {
		t.Error(noClientPIN(fresh), noClientPIN(set))
	}
}

// libfido2's two answers for a touch that never came are ErrNotTouched; a wrong PIN is not.
func TestNotTouched(t *testing.T) {
	for msg, want := range map[string]bool{
		"systemd-cryptenroll: exit status 1: Failed to generate FIDO2 credential: FIDO_ERR_OPERATION_DENIED": true,
		"fido2-cred: exit status 1: fido2-cred: fido_dev_make_cred: FIDO_ERR_ACTION_TIMEOUT":                 true,
		"fido2-cred: exit status 1: fido2-cred: fido_dev_make_cred: FIDO_ERR_PIN_INVALID":                    false,
	} {
		if got := errors.Is(notTouched(errors.New(msg)), ErrNotTouched); got != want {
			t.Errorf("%q: not touched = %v, want %v", msg, got, want)
		}
	}
	if notTouched(nil) != nil {
		t.Error("notTouched(nil) is an error")
	}
}

// A program that failed is named with the last line it wrote to standard error, and with nothing
// after it when it wrote none.
func TestRunSaysWhy(t *testing.T) {
	err := run(context.Background(), nil, nil, "sh", "-c", "echo first >&2; echo FIDO_ERR_ACTION_TIMEOUT >&2; exit 1")
	if err == nil || !strings.HasSuffix(err.Error(), ": FIDO_ERR_ACTION_TIMEOUT") {
		t.Errorf("run = %v", err)
	}
	err = run(context.Background(), nil, nil, "false")
	if err == nil || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("run = %q", err)
	}
}
