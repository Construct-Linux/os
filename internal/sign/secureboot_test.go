package sign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
)

// testKeySet is a key set from three throwaway RSA-2048 keys, and db's key.
func testKeySet(t *testing.T) (map[string][]byte, *rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	var keys [3]*rsa.PrivateKey
	for i := range keys {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = k
	}
	vars, cert, err := SecureBootKeySet(keys[0], keys[1], keys[2])
	if err != nil {
		t.Fatal(err)
	}
	return vars, keys[2], cert
}

// Each variable is signed as the firmware checks it: PK by itself, KEK by PK, db by KEK - and db
// holds Construct's certificate and Microsoft's two option ROM CAs.
func TestTheKeySetEnrollsAsTheFirmwareChecksIt(t *testing.T) {
	vars, _, dbCertificate := testKeySet(t)
	certs := map[string][]*x509.Certificate{}
	for _, name := range SecureBootVariables {
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
		t.Errorf("db holds %d certificates, Construct's first: %v", len(certs["db.auth"]), certs["db.auth"])
	}
}

// A binary db's key signs verifies against db's certificate; signing it twice is refused.
func TestABinaryIsSignedByDBsKey(t *testing.T) {
	_, key, cert := testKeySet(t)
	raw, err := os.ReadFile("testdata/HelloWorld.efi")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := EFI(raw, key, cert)
	if err != nil {
		t.Fatal(err)
	}
	pe, err := authenticode.Parse(bytes.NewReader(signed))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pe.Verify(cert); !ok || err != nil {
		t.Errorf("the signature does not verify: %v", err)
	}
	if _, err := EFI(signed, key, cert); err == nil {
		t.Error("signed a binary twice")
	}
}

// db's key and its certificate come as a pair: a key the certificate is not for is refused, since
// what it signed would boot nowhere.
func TestDBKeyRefusesAnotherKeysCertificate(t *testing.T) {
	_, key, cert := testKeySet(t)
	dir := t.TempDir()
	keyFile, certFile := filepath.Join(dir, "db.pem"), filepath.Join(dir, "db.crt")
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbKeyFiles(t, keyFile, certFile); err != nil {
		t.Fatalf("db's own certificate: %v", err)
	}
	_, _, other := testKeySet(t)
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbKeyFiles(t, keyFile, certFile); err == nil {
		t.Error("another key's certificate: no error")
	}
}

// dbKeyFiles is DBKey with the certificate read from a file, as image usb-sign gives it.
func dbKeyFiles(t *testing.T, keyFile, certFile string) (crypto.Signer, *x509.Certificate, error) {
	t.Helper()
	b, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	return DBKey(context.Background(), keyFile, b)
}
