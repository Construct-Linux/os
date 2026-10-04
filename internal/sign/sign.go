// Package sign is how a release is signed: every key is a crypto.Signer, held either in a file
// (the harness's throwaway keys) or in AWS KMS (a release's, which never leave it).
//
// A key is named by a reference: "kms:<key id, ARN or alias/...>" for KMS, anything else a PEM
// file with a PKCS #8 private key. The kinds are the two the repository signs with: Ed25519
// (releases, OpenPGP) and RSA-2048 (the PCR policy and Secure Boot, where nothing else verifies).
package sign

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

// kmsPrefix marks a reference to a KMS key.
const kmsPrefix = "kms:"

// Open is the signer ref names.
func Open(ctx context.Context, ref string) (crypto.Signer, error) {
	if id, ok := strings.CutPrefix(ref, kmsPrefix); ok {
		return OpenKMS(ctx, id)
	}
	return OpenFile(ref)
}

// IsKMS is whether ref names a KMS key.
func IsKMS(ref string) bool { return strings.HasPrefix(ref, kmsPrefix) }

// OpenFile is the private key in a PEM file: PKCS #8, or PKCS #1 for RSA (openssl's older form).
func OpenFile(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("sign: %s holds no PEM block", path)
	}
	var key any
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("sign: %s holds a %q, not a private key", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("sign: %s: %w", path, err)
	}
	s, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("sign: %s: a %T cannot sign", path, key)
	}
	return s, Allowed(s.Public())
}

// Allowed refuses a key of a kind the repository does not sign with: Ed25519, and RSA of 2048
// bits, the one size a TPM and UEFI firmware both verify.
func Allowed(pub crypto.PublicKey) error {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return nil
	case *rsa.PublicKey:
		if k.N.BitLen() != 2048 {
			return fmt.Errorf("sign: an RSA key of %d bits, want 2048", k.N.BitLen())
		}
		return nil
	}
	return errors.New("sign: a key that is neither Ed25519 nor RSA-2048")
}
