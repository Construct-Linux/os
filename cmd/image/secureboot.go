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
// algorithm firmware and Authenticode agree on. Each key's certificate is made once (image
// secureboot-keys) and kept: it is what machines hold, and a signature under another certificate
// for the same key is refused (internal/sign/secureboot.go). The three variables - PK signs its
// own and KEK's, KEK signs db's - are signed again, under the same certificates, whenever what
// they hold changes. db's key signs every release, on the host (image usb-sign), never in a
// container. The keys are files for the harness's throwaway set and KMS keys for a release's
// (internal/sign); the certificates and the signed variables are public.
//
// systemd-boot enrolls the three variables from the ESP's loader/keys/auto while the firmware is
// in setup mode: by itself in a VM, from its menu on a machine - hidden until a key is held
// (loader.conf's secure-boot-enroll if-safe).

// The key set's public half in its directory: each key's certificate, and the three variables to
// enroll.
const dbCert = "db.crt"

var secureBootCerts = []string{"PK.crt", "KEK.crt", dbCert}

// secureBootKeys makes the key set's public half in -dir from the three keys: a certificate for
// each key that has none there yet, and the three variables, signed again each time. A
// certificate there that is not its key's is refused, with nothing written: the certificate is
// what machines hold, so a new key is a new set every machine must enroll again, a decision to
// make by removing the certificate, never by this command.
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
	var set [3]sign.SecureBootKey
	made := map[string]*x509.Certificate{}
	for i, k := range []struct{ name, ref string }{{"PK", *pkRef}, {"KEK", *kekRef}, {"db", *dbRef}} {
		key, err := sign.OpenRSA(context.Background(), k.ref)
		if err != nil {
			return err
		}
		file := filepath.Join(*dir, secureBootCerts[i])
		b, err := os.ReadFile(file)
		var cert *x509.Certificate
		switch {
		case err == nil:
			if cert, err = sign.ParseCertificateOf(b, key); err != nil {
				return fmt.Errorf("%s: %w: a certificate is its key's for the key's life, and a new key is a new set every machine must enroll again", file, err)
			}
		case os.IsNotExist(err):
			if cert, err = sign.SecureBootCertificate("Construct "+k.name, key); err != nil {
				return err
			}
			made[file] = cert
		default:
			return err
		}
		set[i] = sign.SecureBootKey{Key: key, Cert: cert}
	}
	vars, err := sign.SecureBootKeySet(set[0], set[1], set[2])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	for file, cert := range made {
		if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644); err != nil {
			return err
		}
	}
	for name, b := range vars {
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
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
