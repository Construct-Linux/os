package main

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
)

// Each variable is signed as the firmware checks it: PK by itself, KEK by PK, db by KEK - and db
// holds Spindrift's certificate and Microsoft's two option ROM CAs.
func TestTheKeySetEnrollsAsTheFirmwareChecksIt(t *testing.T) {
	vars, _, dbCertificate, err := makeKeySet()
	if err != nil {
		t.Fatal(err)
	}
	certs := map[string][]*x509.Certificate{}
	for _, name := range sbVariables {
		b := bytes.NewBuffer(vars[name])
		auth, err := signature.ReadEFIVariableAuthencation2(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		db, err := signature.ReadSignatureDatabase(b)
		if err != nil {
			t.Fatalf("%s's list: %v", name, err)
		}
		for _, l := range db {
			for _, s := range l.Signatures {
				c, err := x509.ParseCertificate(s.Data)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				certs[name] = append(certs[name], c)
			}
		}
		signer := map[string]string{"PK.auth": "PK.auth", "KEK.auth": "PK.auth", "db.auth": "KEK.auth"}[name]
		if len(certs[signer]) == 0 {
			t.Fatalf("%s's signer %s has no certificate yet", name, signer)
		}
		if ok, err := auth.Verify(certs[signer][0]); !ok || err != nil {
			t.Errorf("%s is not signed by %s: %v", name, signer, err)
		}
	}
	if len(certs["db.auth"]) != 3 || !certs["db.auth"][0].Equal(dbCertificate) {
		t.Errorf("db holds %d certificates, Spindrift's first: %v", len(certs["db.auth"]), certs["db.auth"])
	}
}

// A binary db's key signs verifies against db's certificate; signing it twice is refused.
func TestABinaryIsSignedByDBsKey(t *testing.T) {
	_, key, cert, err := makeKeySet()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/HelloWorld.efi")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hello.efi")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signPE(path, key, cert); err != nil {
		t.Fatal(err)
	}
	signed, _ := os.ReadFile(path)
	pe, err := authenticode.Parse(bytes.NewReader(signed))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pe.Verify(cert); !ok || err != nil {
		t.Errorf("the signature does not verify: %v", err)
	}
	if err := signPE(path, key, cert); err == nil {
		t.Error("signed a binary twice")
	}
}
