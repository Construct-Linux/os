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
	"sort"
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
	dir := fl.String("dir", "", "the release (default: the newest in _output/release)")
	_ = fl.Parse(args)
	if *ref == "" || *pubring == "" {
		return errors.New("sign-release: -key and -pubring are required")
	}
	if *dir == "" {
		d, err := newestRelease()
		if err != nil {
			return err
		}
		*dir = d
	}
	return signSums(*ref, *pubring, filepath.Join(*dir, "SHA256SUMS"))
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
	if err := sign.Verify(pubring, bytes.NewReader(data), sig); err != nil {
		return fmt.Errorf("the signature over %s does not verify: %w", sums, err)
	}
	return os.WriteFile(sums+".gpg", sig, 0o644)
}

// newestRelease is the newest release image usb-sign wrote.
func newestRelease() (string, error) {
	dirs, err := filepath.Glob(filepath.Join(out, "release", "*"))
	if err != nil || len(dirs) == 0 {
		return "", fmt.Errorf("no release in %s: image usb-sign first", filepath.Join(out, "release"))
	}
	sort.Strings(dirs)
	return dirs[len(dirs)-1], nil
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
