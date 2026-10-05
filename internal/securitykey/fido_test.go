package securitykey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
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

// A new key says noclientPin among its options; one with a PIN says clientPin.
func TestNoClientPIN(t *testing.T) {
	fresh := "proto: 0x02\nmajor: 0x05\noptions: rk, up, noplat, noclientPin, credentialMgmtPreview\n"
	set := "options: rk, up, noplat, clientPin, credentialMgmtPreview\n"
	if !noClientPIN(fresh) || noClientPIN(set) || noClientPIN("aaguid: 00\n") {
		t.Error(noClientPIN(fresh), noClientPIN(set))
	}
}
