package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Construct-Linux/os/internal/sign"
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

// The key set's public half in its directory: db's certificate, and the three variables to enroll.
const dbCert = "db.crt"

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
		s, err := sign.OpenRSA(context.Background(), ref)
		if err != nil {
			return err
		}
		keys[i] = s
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	vars, cert, err := sign.SecureBootKeySet(keys[0], keys[1], keys[2])
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

// dbSigner is db's key (ref) and its certificate (certFile), refusing a key that is not the
// certificate's or not RSA-2048 (sign.DBKey).
func dbSigner(ref, certFile string) (crypto.Signer, *x509.Certificate, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, err
	}
	return sign.DBKey(context.Background(), ref, b)
}
