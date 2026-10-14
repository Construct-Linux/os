package sign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
	"github.com/foxboron/go-uefi/efi/util"
	"github.com/foxboron/go-uefi/efivar"
)

// Secure Boot: the firmware runs only EFI binaries a certificate in its db signed. The keys are
// RSA-2048, PKCS #1 v1.5 over SHA-256 - the one algorithm firmware and Authenticode agree on.
//
// What a machine holds is the certificate, not the key: the firmware verifies a signature's
// chain against the certificates in its variables, byte for byte, and a new self-signed
// certificate for the same key is a stranger to it (release 20261014.1's kernel image, signed
// under a db certificate made again for the unchanged KMS key, was refused by every machine that
// had enrolled the first one). So a certificate is made once for a key and kept for the key's
// life, beside the public halves, and what changes over time is the variables, signed again
// under the same certificates: a KEK or db that holds more, or less, is enrolled by the machines
// that hold the PK and KEK that signed it, and the kernel images they boot stay the same.

// db holds Microsoft's two CAs for option ROMs beside Construct's key: a graphics card's or
// network card's firmware is signed by Microsoft (the 2011 UEFI CA, and since 2023 its Option ROM
// CA), and a db without them leaves such a machine without a screen, or without a boot. Not the
// Windows CAs, nor 2023's for others' boot loaders.
//
// KEK holds Microsoft's two KEK CAs beside Construct's key, for dbx: the 2011 UEFI CA in db also
// signs every distribution's shim, so a boot loader Microsoft has revoked (BootHole's) boots here
// until its revocation is in dbx, and Microsoft's dbx updates are signed under its KEK CAs - the
// 2011 one so far, its 2023 successor next. fwupd applies them (uefi_dbx). The same CAs sign
// Microsoft's db additions, which fwupd is told not to apply (image/rootfs/etc/fwupd/fwupd.conf):
// what they would add, root could enroll with any shim of today's db already.
//
// From github.com/microsoft/secureboot_objects (PreSignedObjects/{DB,KEK}/Certificates,
// fcbc3fdc); firmware does not read a certificate's dates.
//
//go:embed secureboot/*.der
var microsoftCAs embed.FS

var (
	microsoftKEKCAs = []string{"microsoft-kek-ca-2011.der", "microsoft-kek-2k-ca-2023.der"}
	microsoftDBCAs  = []string{"microsoft-uefi-ca-2011.der", "microsoft-option-rom-uefi-ca-2023.der"}
)

// microsoftCerts are the named embedded certificates, in their order.
func microsoftCerts(names []string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for _, n := range names {
		raw, err := microsoftCAs.ReadFile("secureboot/" + n)
		if err != nil {
			return nil, err
		}
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// SecureBootVariables are the signed variables SecureBootKeySet makes, by file name, in the order
// they are enrolled. A function, so no caller can reorder or drop one for every other caller.
func SecureBootVariables() []string { return []string{"PK.auth", "KEK.auth", "db.auth"} }

// owner is the owner GUID of the signatures Construct adds.
var owner = *util.StringToGUID("0239b3ac-9f59-4d16-84ae-cdd4977061a5")

// SecureBootKey is one of the set's keys with its certificate, the one machines hold for it
// (SecureBootCertificate, once).
type SecureBootKey struct {
	Key  crypto.Signer
	Cert *x509.Certificate
}

// SecureBootKeySet signs Secure Boot's three variables, by file name (PK.auth, KEK.auth,
// db.auth), from the key set: PK signs itself and KEK; KEK holds Construct's certificate and
// Microsoft's KEK CAs, and signs db, which holds Construct's certificate and Microsoft's option
// ROM CAs. Construct's comes first in each. Signed again whenever a variable's content changes,
// under the certificates machines hold: a key whose certificate is not its own is refused.
func SecureBootKeySet(pk, kek, db SecureBootKey) (map[string][]byte, error) {
	for _, k := range []struct {
		name string
		key  SecureBootKey
	}{{"PK", pk}, {"KEK", kek}, {"db", db}} {
		if err := certificateOf(k.key.Cert, k.key.Key); err != nil {
			return nil, fmt.Errorf("%s: %w", k.name, err)
		}
	}
	lists, err := secureBootLists(pk.Cert, kek.Cert, db.Cert)
	if err != nil {
		return nil, err
	}
	vars := map[string][]byte{}
	for _, v := range []struct {
		file string
		v    efivar.Efivar
		by   SecureBootKey
	}{
		{"PK.auth", efivar.PK, pk},
		{"KEK.auth", efivar.KEK, pk},
		{"db.auth", efivar.Db, kek},
	} {
		list := signature.NewSignatureDatabase()
		for _, c := range lists[v.file] {
			if err := list.Append(signature.CERT_X509_GUID, owner, c.Raw); err != nil {
				return nil, err
			}
		}
		_, signed, err := signature.SignEFIVariable(v.v, list, v.by.Key, v.by.Cert)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", v.file, err)
		}
		var b bytes.Buffer
		signed.Marshal(&b)
		vars[v.file] = b.Bytes()
	}
	return vars, nil
}

// secureBootLists are the certificates each variable holds, in order, by file name.
func secureBootLists(pk, kek, db *x509.Certificate) (map[string][]*x509.Certificate, error) {
	kekCAs, err := microsoftCerts(microsoftKEKCAs)
	if err != nil {
		return nil, err
	}
	dbCAs, err := microsoftCerts(microsoftDBCAs)
	if err != nil {
		return nil, err
	}
	return map[string][]*x509.Certificate{
		"PK.auth":  {pk},
		"KEK.auth": append([]*x509.Certificate{kek}, kekCAs...),
		"db.auth":  append([]*x509.Certificate{db}, dbCAs...),
	}, nil
}

// VerifySecureBootKeySet checks the signed variables vars (by file name) as the firmware does
// when it enrolls them: each holds exactly what SecureBootKeySet puts in it under the
// certificates pk, kek and db, in that order, and is signed by its signer's - PK by itself, KEK
// by PK, db by KEK. What a build ships for machines to enroll is checked with it against the
// certificates the releases are signed under.
func VerifySecureBootKeySet(vars map[string][]byte, pk, kek, db *x509.Certificate) error {
	lists, err := secureBootLists(pk, kek, db)
	if err != nil {
		return err
	}
	signer := map[string]*x509.Certificate{"PK.auth": pk, "KEK.auth": pk, "db.auth": kek}
	for _, name := range SecureBootVariables() {
		raw, ok := vars[name]
		if !ok {
			return fmt.Errorf("%s: missing", name)
		}
		b := bytes.NewBuffer(raw)
		auth, err := signature.ReadEFIVariableAuthencation2(b)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sd, err := signature.ReadSignatureDatabase(b)
		if err != nil {
			return fmt.Errorf("%s's list: %w", name, err)
		}
		var held []*x509.Certificate
		for _, l := range sd {
			if l.SignatureType != signature.CERT_X509_GUID {
				return fmt.Errorf("%s holds a signature that is not a certificate", name)
			}
			for _, s := range l.Signatures {
				c, err := x509.ParseCertificate(s.Data)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				held = append(held, c)
			}
		}
		want := lists[name]
		if len(held) != len(want) {
			return fmt.Errorf("%s holds %d certificates, not %d", name, len(held), len(want))
		}
		for i := range want {
			if !held[i].Equal(want[i]) {
				return fmt.Errorf("%s's certificate %d is %q, not %q", name, i, held[i].Subject.CommonName, want[i].Subject.CommonName)
			}
		}
		if ok, err := auth.Verify(signer[name]); !ok || err != nil {
			return fmt.Errorf("%s is not signed by %s: %v", name, signer[name].Subject.CommonName, err)
		}
	}
	return nil
}

// SecureBootCertificate is a self-signed certificate for key, made once in the key's life: it is
// what machines enroll and what a signature is matched against, so a second one for the same key
// signs for nobody who holds the first. The name says whose it is to a person who dumps the
// variables; the firmware reads neither it nor the dates.
func SecureBootCertificate(name string, key crypto.Signer) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: name, Organization: []string{"Construct"}},
		NotBefore:          now.Add(-time.Hour),
		NotAfter:           now.AddDate(30, 0, 0),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// ParseCertificateOf is the certificate in certPEM, refusing one that is not key's: what the key
// signs under another's certificate boots nowhere.
func ParseCertificateOf(certPEM []byte, key crypto.Signer) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, certificateOf(cert, key)
}

// certificateOf is nil when cert is key's.
func certificateOf(cert *x509.Certificate, key crypto.Signer) error {
	if cert == nil {
		return errors.New("no certificate")
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(key.Public()) {
		return errors.New("the key is not its certificate's: what it signs boots nowhere")
	}
	return nil
}

// EFI is the EFI binary raw signed with db's key: the Authenticode signature firmware checks against
// db before it runs it. A binary signed already is refused, not signed twice.
func EFI(raw []byte, key crypto.Signer, cert *x509.Certificate) ([]byte, error) {
	pe, err := authenticode.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("not a PE image: %w", err)
	}
	sigs, err := pe.Signatures()
	if err != nil {
		return nil, fmt.Errorf("its certificate table: %w", err)
	}
	if len(sigs) > 0 {
		return nil, errors.New("signed already")
	}
	if _, err := pe.Sign(key, cert); err != nil {
		return nil, err
	}
	return pe.Bytes(), nil
}

// DBKey is db's key, ref, with its certificate (PEM), refusing a key that is not the certificate's
// or not RSA-2048: what it signed would boot nowhere.
func DBKey(ctx context.Context, ref string, certPEM []byte) (crypto.Signer, *x509.Certificate, error) {
	key, err := OpenRSA(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	cert, err := ParseCertificateOf(certPEM, key)
	if err != nil {
		return nil, nil, fmt.Errorf("db's certificate: %w", err)
	}
	return key, cert, nil
}
