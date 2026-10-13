package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Construct-Linux/os/internal/sign"
	"github.com/Construct-Linux/os/internal/update"
)

// The release key and what is public of every key. A release's SHA256SUMS is signed by the
// release key - Ed25519, OpenPGP, in a file for the harness and in KMS for a release
// (internal/sign) - and machines trust its public key, release.pgp, which is made once from the
// key (image release-key) and kept beside it: its fingerprint covers its creation time, so it is
// never made again at signing.

// releaseKey writes the OpenPGP public key of the release key ref to -o, once.
func releaseKey(args []string) error {
	fl := flag.NewFlagSet("release-key", flag.ExitOnError)
	ref := fl.String("key", "", "the release key, Ed25519: a PEM file, or kms:<key>")
	uid := fl.String("uid", "Construct release", "the key's user ID")
	to := fl.String("o", "", "the public key (release.pgp) to write")
	_ = fl.Parse(args)
	if *ref == "" || *to == "" {
		return errors.New("release-key: -key and -o are required")
	}
	if _, err := os.Stat(*to); err == nil {
		return fmt.Errorf("%s exists: a new one is another key, which no machine trusts", *to)
	}
	s, err := sign.Open(context.Background(), *ref)
	if err != nil {
		return err
	}
	k, err := sign.NewKey(s, time.Now())
	if err != nil {
		return err
	}
	pubring, err := k.Export(*uid)
	if err != nil {
		return err
	}
	// What machines will hold is checked by the image's own policy before it is written.
	if _, err := update.ParseKeyring(pubring); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*to, pubring, 0o644)
}

// signRelease signs a release's SHA256SUMS as SHA256SUMS.gpg with the release key, and checks the
// signature against release.pgp before it is written.
func signRelease(args []string) error {
	fl := flag.NewFlagSet("sign-release", flag.ExitOnError)
	ref := fl.String("key", "", "the release key: a PEM file, or kms:<key>")
	pubring := fl.String("pubring", "", "its public key (release.pgp)")
	dir := fl.String("dir", "", "the release (default: the one in _output/release)")
	_ = fl.Parse(args)
	if *ref == "" || *pubring == "" {
		return errors.New("sign-release: -key and -pubring are required")
	}
	if *dir == "" {
		v, err := currentRelease()
		if err != nil {
			return err
		}
		*dir = releaseDir(v)
	}
	if err := refuseKMSRelease(*dir, *ref); err != nil {
		return err
	}
	return signSums(*ref, *pubring, filepath.Join(*dir, "SHA256SUMS"))
}

// refuseKMSRelease holds a release directory's SHA256SUMS to what sign-pcr and usb-sign hold the
// build to: a release's key (KMS) signs it only as the release image usb last built, which
// refuseKMS takes. A channel's or dl's index, also signed by sign-release, is no release
// directory.
func refuseKMSRelease(dir, ref string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	releases, err := filepath.Abs(filepath.Join(out, "release"))
	if err != nil {
		return err
	}
	if !sign.IsKMS(ref) || filepath.Dir(abs) != releases {
		return nil
	}
	b, err := readBuilt(filepath.Join(out, "usb"))
	if err != nil {
		return err
	}
	if b.Version != filepath.Base(abs) {
		return fmt.Errorf("%s is not the release image usb last built (%s): it is not signed with a release's key", filepath.Base(abs), b.Version)
	}
	return refuseKMS(b, ref)
}

// signSums signs a SHA256SUMS file - a release's, or a channel's index - next to it.
func signSums(ref, pubringFile, sums string) error {
	pubring, err := os.ReadFile(pubringFile)
	if err != nil {
		return err
	}
	s, err := sign.Open(context.Background(), ref)
	if err != nil {
		return err
	}
	k, err := sign.ReadKey(s, pubring)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	sig, err := k.DetachSign(bytes.NewReader(data), time.Now())
	if err != nil {
		return err
	}
	// Checked as a machine checks it (internal/update): the keyring it carries, Ed25519 alone.
	keys, err := update.ParseKeyring(pubring)
	if err != nil {
		return fmt.Errorf("%s: %w", pubringFile, err)
	}
	if err := update.Verify(keys, data, sig); err != nil {
		return fmt.Errorf("the signature over %s does not verify: %w", sums, err)
	}
	return os.WriteFile(sums+".gpg", sig, 0o644)
}

// publicKey writes the public half of the key ref as PEM: the PCR key's, which the kernel image
// carries for systemd to check its policy's signatures with.
func publicKey(args []string) error {
	fl := flag.NewFlagSet("public-key", flag.ExitOnError)
	ref := fl.String("key", "", "a PEM file, or kms:<key>")
	to := fl.String("o", "", "the public key to write")
	_ = fl.Parse(args)
	if *ref == "" || *to == "" {
		return errors.New("public-key: -key and -o are required")
	}
	s, err := sign.Open(context.Background(), *ref)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(s.Public())
	if err != nil {
		return err
	}
	return os.WriteFile(*to, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)
}

// packagesList writes the baked root's packages as packages.txt (writePackages): a release asset,
// which the next release's notes compare against.
func packagesList(args []string) error {
	fl := flag.NewFlagSet("packages-list", flag.ExitOnError)
	// The baked root.
	root := filepath.Join(out, "usb", "root")
	to := fl.String("o", "", "packages.txt to write")
	_ = fl.Parse(args)
	if *to == "" {
		return errors.New("packages-list: -o is required")
	}
	return writePackages(filepath.Join(root, "usr/lib/apk/db/installed"), *to)
}
