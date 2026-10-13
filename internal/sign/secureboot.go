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
	"io/fs"
	"math/big"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
	"github.com/foxboron/go-uefi/efi/util"
	"github.com/foxboron/go-uefi/efivar"
)

// Secure Boot: the firmware runs only EFI binaries a certificate in its db signed. The keys are
// RSA-2048, PKCS #1 v1.5 over SHA-256 - the one algorithm firmware and Authenticode agree on.

// db holds Microsoft's two CAs for option ROMs beside Construct's key: a graphics card's or
// network card's firmware is signed by Microsoft (the 2011 UEFI CA, and since 2023 its Option ROM
// CA), and a db without them leaves such a machine without a screen, or without a boot. Not the
// Windows CAs, nor 2023's for others' boot loaders. From github.com/microsoft/secureboot_objects
// (PreSignedObjects/DB/Certificates, fcbc3fdc); firmware does not read a db certificate's dates.
//
//go:embed secureboot/*.der
var microsoftCAs embed.FS

// SecureBootVariables are the signed variables SecureBootKeySet makes, by file name, in the order
// they are enrolled. A function, so no caller can reorder or drop one for every other caller.
func SecureBootVariables() []string { return []string{"PK.auth", "KEK.auth", "db.auth"} }

// owner is the owner GUID of the signatures Construct adds.
var owner = *util.StringToGUID("0239b3ac-9f59-4d16-84ae-cdd4977061a5")

// SecureBootKeySet makes Secure Boot's key set from its three keys, PK, KEK and db: the three
// signed variables to enroll, by file name (PK.auth, KEK.auth, db.auth), and db's certificate.
// PK signs itself and KEK, KEK signs db, and db holds Construct's certificate and Microsoft's
// option ROM CAs.
func SecureBootKeySet(pk, kek, db crypto.Signer) (map[string][]byte, *x509.Certificate, error) {
	pkCert, err := newCert("Construct PK", pk)
	if err != nil {
		return nil, nil, err
	}
	kekCert, err := newCert("Construct KEK", kek)
	if err != nil {
		return nil, nil, err
	}
	dbCertificate, err := newCert("Construct db", db)
	if err != nil {
		return nil, nil, err
	}
	dbList := []*x509.Certificate{dbCertificate}
	cas, err := fs.Glob(microsoftCAs, "secureboot/*.der")
	if err != nil || len(cas) != 2 {
		return nil, nil, fmt.Errorf("sign: Microsoft's option ROM CAs: %v %v", cas, err)
	}
	for _, f := range cas {
		raw, err := microsoftCAs.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f, err)
		}
		dbList = append(dbList, c)
	}
	vars := map[string][]byte{}
	// PK signs itself and KEK; KEK signs db.
	for _, v := range []struct {
		file   string
		v      efivar.Efivar
		certs  []*x509.Certificate
		signer crypto.Signer
		by     *x509.Certificate
	}{
		{"PK.auth", efivar.PK, []*x509.Certificate{pkCert}, pk, pkCert},
		{"KEK.auth", efivar.KEK, []*x509.Certificate{kekCert}, pk, pkCert},
		{"db.auth", efivar.Db, dbList, kek, kekCert},
	} {
		list := signature.NewSignatureDatabase()
		for _, c := range v.certs {
			if err := list.Append(signature.CERT_X509_GUID, owner, c.Raw); err != nil {
				return nil, nil, err
			}
		}
		_, signed, err := signature.SignEFIVariable(v.v, list, v.signer, v.by)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", v.file, err)
		}
		var b bytes.Buffer
		signed.Marshal(&b)
		vars[v.file] = b.Bytes()
	}
	return vars, dbCertificate, nil
}

// newCert is a self-signed certificate for key. Firmware checks a signature against the
// certificate's key and reads nothing else of it; the name says whose it is to a person who dumps
// the variables.
func newCert(name string, key crypto.Signer) (*x509.Certificate, error) {
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
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, errors.New("db's certificate: no PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(key.Public()) {
		return nil, nil, errors.New("db's key is not its certificate's: what it signs boots nowhere")
	}
	return key, cert, nil
}
