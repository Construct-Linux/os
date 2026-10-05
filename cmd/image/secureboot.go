package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/efi/signature"
	"github.com/foxboron/go-uefi/efi/util"
	"github.com/foxboron/go-uefi/efivar"
)

// Secure Boot. The firmware runs only EFI binaries a certificate in its db signed: systemd-boot
// and the kernel image, whose command line names the root's dm-verity hash, so one signature
// holds the whole boot to Construct's. The keys are RSA-2048, PKCS #1 v1.5 over SHA-256 - the one
// algorithm firmware and Authenticode agree on - and the key set is made once (image
// secureboot-keys): PK signs its own variable and KEK's, KEK signs db's, and they sign nothing
// after, unless db changes. db's key signs every release, on the host (image usb-sign), never in a
// container. The keys are files for the harness's throwaway set and KMS keys for a release's
// (internal/sign); the certificates and the signed variables are public.
//
// systemd-boot enrolls the three variables from the ESP's loader/keys/auto while the firmware is
// in setup mode: by itself in a VM, from its menu on a machine (loader.conf's secure-boot-enroll
// if-safe).

// db holds Microsoft's two CAs for option ROMs beside Construct's key: a graphics card's or
// network card's firmware is signed by Microsoft (the 2011 UEFI CA, and since 2023 its Option ROM
// CA), and a db without them leaves such a machine without a screen, or without a boot. Not the
// Windows CAs, nor 2023's for others' boot loaders. From github.com/microsoft/secureboot_objects
// (PreSignedObjects/DB/Certificates, fcbc3fdc); firmware does not read a db certificate's dates.
//
//go:embed secureboot/*.der
var microsoftCAs embed.FS

// sbOwner is the owner GUID of the signatures Construct adds.
var sbOwner = *util.StringToGUID("0239b3ac-9f59-4d16-84ae-cdd4977061a5")

// The key set's public half in its directory: db's certificate, and the three variables to enroll.
const dbCert = "db.crt"

var sbVariables = []string{"PK.auth", "KEK.auth", "db.auth"}

// secureBootKeys makes the key set's public half in -dir from the three keys: refused if one is
// there, which is a decision to replace.
func secureBootKeys(args []string) error {
	fl := flag.NewFlagSet("secureboot-keys", flag.ExitOnError)
	dir := fl.String("dir", "", "where the certificates and signed variables go")
	pkRef := fl.String("pk", "", "PK: a PEM file, or kms:<key>")
	kekRef := fl.String("kek", "", "KEK: a PEM file, or kms:<key>")
	dbRef := fl.String("db", "", "db: a PEM file, or kms:<key>")
	_ = fl.Parse(args)
	if *dir == "" || *pkRef == "" || *kekRef == "" || *dbRef == "" {
		return errors.New("secureboot-keys: -dir, -pk, -kek and -db are required")
	}
	if _, err := os.Stat(filepath.Join(*dir, dbCert)); err == nil {
		return fmt.Errorf("%s holds a key set already: a new one is a new db, which every machine must enroll again", *dir)
	}
	var keys [3]crypto.Signer
	for i, ref := range []string{*pkRef, *kekRef, *dbRef} {
		s, err := rsaSigner(ref)
		if err != nil {
			return err
		}
		keys[i] = s
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	vars, cert, err := makeKeySet(keys[0], keys[1], keys[2])
	if err != nil {
		return err
	}
	for name, b := range vars {
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(*dir, dbCert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644)
}

// makeKeySet answers the three signed variables, by file name, and db's certificate.
func makeKeySet(pk, kek, db crypto.Signer) (map[string][]byte, *x509.Certificate, error) {
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
		return nil, nil, fmt.Errorf("Microsoft's option ROM CAs: %v %v", cas, err)
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
			if err := list.Append(signature.CERT_X509_GUID, sbOwner, c.Raw); err != nil {
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

// dbSigner is db's key (ref) and its certificate (certFile), refusing a key that is not the
// certificate's or not RSA-2048.
func dbSigner(ref, certFile string) (crypto.Signer, *x509.Certificate, error) {
	key, err := rsaSigner(ref)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM", certFile)
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

// signEFI is the EFI binary raw signed: the Authenticode signature firmware checks against
// db before it runs it. A binary signed already is refused, not signed twice.
func signEFI(raw []byte, key crypto.Signer, cert *x509.Certificate) ([]byte, error) {
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
