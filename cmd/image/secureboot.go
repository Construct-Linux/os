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
// holds the whole boot to Spindrift's. The keys are RSA-2048, PKCS #1 v1.5 over SHA-256 - the one
// algorithm firmware and Authenticode agree on - and made once on the host (image
// secureboot-keys). PK and KEK sign their own variables and db's then and are thrown away: they
// would only ever sign a change to db, and a new db is a new key set. db's key stays in
// SIGNING_HOME, never in a container, and signs on the host (image usb-sign).
//
// systemd-boot enrolls the three variables from the ESP's loader/keys/auto while the firmware is
// in setup mode: by itself in a VM, from its menu on a machine (loader.conf's secure-boot-enroll
// if-safe).

// db holds Microsoft's two CAs for option ROMs beside Spindrift's key: a graphics card's or
// network card's firmware is signed by Microsoft (the 2011 UEFI CA, and since 2023 its Option ROM
// CA), and a db without them leaves such a machine without a screen, or without a boot. Not the
// Windows CAs, nor 2023's for others' boot loaders. From github.com/microsoft/secureboot_objects
// (PreSignedObjects/DB/Certificates, fcbc3fdc); firmware does not read a db certificate's dates.
//
//go:embed secureboot/*.der
var microsoftCAs embed.FS

// sbOwner is the owner GUID of the signatures Spindrift adds.
var sbOwner = *util.StringToGUID("0239b3ac-9f59-4d16-84ae-cdd4977061a5")

// The key set in its directory: db's key and certificate, and the three variables to enroll.
const (
	dbKey  = "db.key"
	dbCert = "db.crt"
)

var sbVariables = []string{"PK.auth", "KEK.auth", "db.auth"}

// secureBootKeys makes the key set in -dir: refused if one is there, which is a decision to
// replace.
func secureBootKeys(args []string) error {
	fl := flag.NewFlagSet("secureboot-keys", flag.ExitOnError)
	dir := fl.String("dir", "", "where the key set goes (SIGNING_HOME/secureboot)")
	_ = fl.Parse(args)
	if *dir == "" {
		return errors.New("secureboot-keys: -dir is required")
	}
	if _, err := os.Stat(filepath.Join(*dir, dbKey)); err == nil {
		return fmt.Errorf("%s holds a key set already: a new one is a new db, which every machine must enroll again", *dir)
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	vars, key, cert, err := makeKeySet()
	if err != nil {
		return err
	}
	for name, b := range vars {
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			return err
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, dbKey), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, dbCert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644)
}

// makeKeySet answers the three signed variables, by file name, and db's key and certificate.
func makeKeySet() (map[string][]byte, *rsa.PrivateKey, *x509.Certificate, error) {
	pk, pkCert, err := newKey("Spindrift PK")
	if err != nil {
		return nil, nil, nil, err
	}
	kek, kekCert, err := newKey("Spindrift KEK")
	if err != nil {
		return nil, nil, nil, err
	}
	db, dbCertificate, err := newKey("Spindrift db")
	if err != nil {
		return nil, nil, nil, err
	}
	dbList := []*x509.Certificate{dbCertificate}
	cas, err := fs.Glob(microsoftCAs, "secureboot/*.der")
	if err != nil || len(cas) != 2 {
		return nil, nil, nil, fmt.Errorf("Microsoft's option ROM CAs: %v %v", cas, err)
	}
	for _, f := range cas {
		raw, err := microsoftCAs.ReadFile(f)
		if err != nil {
			return nil, nil, nil, err
		}
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", f, err)
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
				return nil, nil, nil, err
			}
		}
		_, signed, err := signature.SignEFIVariable(v.v, list, v.signer, v.by)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", v.file, err)
		}
		var b bytes.Buffer
		signed.Marshal(&b)
		vars[v.file] = b.Bytes()
	}
	return vars, db, dbCertificate, nil
}

// newKey is an RSA-2048 key and a self-signed certificate for it. Firmware checks a signature
// against the certificate's key and reads nothing else of it; the name says whose it is to a
// person who dumps the variables.
func newKey(name string) (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: name, Organization: []string{"Spindrift"}},
		NotBefore:          now.Add(-time.Hour),
		NotAfter:           now.AddDate(30, 0, 0),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return key, cert, err
}

// readDBKey reads db's key and certificate from the key set in dir, refusing a key that is not the
// certificate's or not RSA-2048.
func readDBKey(dir string) (*rsa.PrivateKey, *x509.Certificate, error) {
	b, err := os.ReadFile(filepath.Join(dir, dbKey))
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM", dbKey)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := k.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() != 2048 {
		return nil, nil, fmt.Errorf("%s is not an RSA-2048 key", dbKey)
	}
	b, err = os.ReadFile(filepath.Join(dir, dbCert))
	if err != nil {
		return nil, nil, err
	}
	block, _ = pem.Decode(b)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: no PEM", dbCert)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, errors.New("db's key is not its certificate's: what it signs boots nowhere")
	}
	return key, cert, nil
}

// signPE signs the EFI binary at path in place: the Authenticode signature firmware checks
// against db before it runs it. A binary signed already is refused, not signed twice.
func signPE(path string, key crypto.Signer, cert *x509.Certificate) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pe, err := authenticode.Parse(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s is not a PE image: %w", path, err)
	}
	sigs, err := pe.Signatures()
	if err != nil {
		return fmt.Errorf("%s's certificate table: %w", path, err)
	}
	if len(sigs) > 0 {
		return fmt.Errorf("%s is signed already", path)
	}
	if _, err := pe.Sign(key, cert); err != nil {
		return fmt.Errorf("signing %s: %w", path, err)
	}
	return os.WriteFile(path, pe.Bytes(), 0o644)
}
