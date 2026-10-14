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
	"slices"
	"testing"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
	"github.com/foxboron/go-uefi/pkcs7"
)

// testKeys are three throwaway RSA-2048 keys with their certificates: PK, KEK and db.
func testKeys(t *testing.T) (pk, kek, db SecureBootKey) {
	t.Helper()
	keys := []*SecureBootKey{&pk, &kek, &db}
	for i, name := range []string{"Construct PK", "Construct KEK", "Construct db"} {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		c, err := SecureBootCertificate(name, k)
		if err != nil {
			t.Fatal(err)
		}
		*keys[i] = SecureBootKey{Key: k, Cert: c}
	}
	return pk, kek, db
}

// testKeySet is a key set from three throwaway keys, and db's key with its certificate.
func testKeySet(t *testing.T) (map[string][]byte, *rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	pk, kek, db := testKeys(t)
	vars, err := SecureBootKeySet(pk, kek, db)
	if err != nil {
		t.Fatal(err)
	}
	return vars, db.Key.(*rsa.PrivateKey), db.Cert
}

// Each variable is signed as the firmware checks it: PK by itself, KEK by PK, db by KEK - KEK
// holds Construct's certificate and Microsoft's two KEK CAs, db Construct's and Microsoft's two
// option ROM CAs, Construct's first in each. Signed again, the set is the same set: the
// certificates machines hold, so a machine that enrolled one enrolls the next. Under another
// key's certificate nothing is signed.
func TestTheKeySetEnrollsAsTheFirmwareChecksIt(t *testing.T) {
	pk, kek, db := testKeys(t)
	vars, err := SecureBootKeySet(pk, kek, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecureBootKeySet(vars, pk.Cert, kek.Cert, db.Cert); err != nil {
		t.Error(err)
	}
	names := func(name string) (n []string) {
		b := bytes.NewBuffer(vars[name])
		if _, err := signature.ReadEFIVariableAuthencation2(b); err != nil {
			t.Fatal(err)
		}
		sd, err := signature.ReadSignatureDatabase(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range sd {
			for _, s := range l.Signatures {
				c, err := x509.ParseCertificate(s.Data)
				if err != nil {
					t.Fatal(err)
				}
				n = append(n, c.Subject.CommonName)
			}
		}
		return n
	}
	if got := names("KEK.auth"); !slices.Equal(got, []string{"Construct KEK", "Microsoft Corporation KEK CA 2011", "Microsoft Corporation KEK 2K CA 2023"}) {
		t.Errorf("KEK holds %v", got)
	}
	if got := names("db.auth"); !slices.Equal(got, []string{"Construct db", "Microsoft Corporation UEFI CA 2011", "Microsoft Option ROM UEFI CA 2023"}) {
		t.Errorf("db holds %v", got)
	}
	again, err := SecureBootKeySet(pk, kek, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecureBootKeySet(again, pk.Cert, kek.Cert, db.Cert); err != nil {
		t.Errorf("signed again: %v", err)
	}
	_, _, other := testKeys(t)
	if err := VerifySecureBootKeySet(vars, pk.Cert, kek.Cert, other.Cert); err == nil {
		t.Error("a db of another certificate verified")
	}
	if _, err := SecureBootKeySet(pk, kek, SecureBootKey{Key: db.Key, Cert: other.Cert}); err == nil {
		t.Error("db's key under another's certificate: no error")
	}
}

// Microsoft's dbx update (secureboot_objects PostSignedObjects/DBX/amd64/DBXUpdate.bin, fcbc3fdc)
// is signed by a key whose certificate a CA in the KEK issued: the firmware takes it, so fwupd's
// revocations land. The chain is verified at the signing time: the 2011 CA's dates have passed,
// and firmware reads no dates. The signature itself is over the variable's name, GUID,
// attributes, time and data, which only the firmware has together.
func TestMicrosoftsDBXUpdateIsSignedUnderTheKEK(t *testing.T) {
	vars, _, _ := testKeySet(t)
	b := bytes.NewBuffer(vars["KEK.auth"])
	if _, err := signature.ReadEFIVariableAuthencation2(b); err != nil {
		t.Fatal(err)
	}
	kek, err := signature.ReadSignatureDatabase(b)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	for _, l := range kek {
		for _, s := range l.Signatures {
			c, err := x509.ParseCertificate(s.Data)
			if err != nil {
				t.Fatal(err)
			}
			roots.AddCert(c)
		}
	}
	raw, err := os.ReadFile("testdata/DBXUpdate.bin")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := signature.ReadEFIVariableAuthencation2(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	p7, err := pkcs7.ParsePKCS7(auth.AuthInfo.CertData)
	if err != nil {
		t.Fatal(err)
	}
	var signer *x509.Certificate
	for _, c := range p7.Certs {
		if p7.HasCertificate(c) {
			signer = c
		}
	}
	if signer == nil {
		t.Fatal("the update carries no certificate of its signer")
	}
	inter := x509.NewCertPool()
	for _, c := range p7.Certs {
		inter.AddCert(c)
	}
	if _, err := signer.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: signer.NotBefore.Add(time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("%s does not chain to the KEK: %v", signer.Subject.CommonName, err)
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
