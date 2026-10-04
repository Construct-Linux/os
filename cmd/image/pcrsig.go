package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Construct-Linux/os/internal/sign"
)

// The kernel image's PCR 11 policy, signed offline. An installed machine's /var is sealed in the
// TPM to whatever PCR 11 policy the PCR key signed (cmd/construct makeVar), so a kernel image this
// key did not sign never opens it. The key is RSA-2048 - the one exception to Ed25519: a TPM cannot
// verify Ed25519, and systemd verifies a PCR policy's signature as RSA only (RSASSA, SHA-256) - and
// like the release key it never enters a container: ukify computes the policy's digests in
// build/tools' container (image usb), the host signs them here (image sign-pcr) with the key in a
// file or in KMS (internal/sign), and ukify joins the signature to the kernel image (image
// usb-join).

// Where the stage holds the policy between those steps.
const (
	pcrDigests   = "pcr-digests.json"
	pcrSignature = "pcr-signature.json"
	pcrPublic    = "pcr-public.pem"
)

// pcrPolicy is systemd-measure's JSON: for each bank, one entry per boot phase.
type pcrPolicy map[string][]struct {
	PCRs        []int  `json:"pcrs"`
	Fingerprint string `json:"pkfp"`
	Policy      string `json:"pol"`
	Signature   string `json:"sig,omitempty"`
}

// signPCR signs the digests the last image usb left in its stage with the key in args.
func signPCR(args []string) error {
	fl := flag.NewFlagSet("sign-pcr", flag.ExitOnError)
	ref := fl.String("key", "", "the PCR key, RSA-2048: a PEM file, or kms:<key>")
	_ = fl.Parse(args)
	stage := filepath.Join(out, "usb")
	key, err := rsaSigner(*ref)
	if err != nil {
		return err
	}
	digests, err := os.ReadFile(filepath.Join(stage, pcrDigests))
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(filepath.Join(stage, pcrPublic))
	if err != nil {
		return err
	}
	signed, err := signPolicy(digests, key, pub)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, pcrSignature), signed, 0o644)
}

// rsaSigner is the RSA-2048 key ref names (internal/sign), refusing any other kind: the PCR
// policy and Secure Boot are verified as RSA alone.
func rsaSigner(ref string) (crypto.Signer, error) {
	s, err := sign.Open(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	if _, ok := s.Public().(*rsa.PublicKey); !ok {
		return nil, fmt.Errorf("%s is not an RSA-2048 key", ref)
	}
	return s, nil
}

// signPolicy signs every boot phase's policy in every bank, as ukify does by default: systemd
// seals its own secrets to the signed policy too (systemd-tpm2-setup's NvPCR anchor, made in the
// initrd and opened in the root), so a phase left unsigned breaks them there. /var's key still
// needs the person's PIN in any phase. systemd checks a PKCS#1 v1.5 signature of the policy's
// SHA-256; each is checked here against pub, the key the kernel image carries, which a wrong key
// would fail.
func signPolicy(digests []byte, key crypto.Signer, pub []byte) ([]byte, error) {
	var p pcrPolicy
	if err := json.Unmarshal(digests, &p); err != nil {
		return nil, fmt.Errorf("the PCR policy's digests: %w", err)
	}
	block, _ := pem.Decode(pub)
	if block == nil {
		return nil, errors.New("the kernel image's PCR key: no PEM")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	carried, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("the kernel image's PCR key is not RSA")
	}
	if len(p) == 0 {
		return nil, errors.New("the PCR policy has no bank")
	}
	for bank, phases := range p {
		if len(phases) == 0 {
			return nil, fmt.Errorf("bank %s has no phase", bank)
		}
		for i := range phases {
			e := &phases[i]
			pol, err := hex.DecodeString(e.Policy)
			if err != nil || len(pol) == 0 {
				return nil, fmt.Errorf("bank %s: policy %q", bank, e.Policy)
			}
			h := sha256.Sum256(pol)
			sig, err := key.Sign(rand.Reader, h[:], crypto.SHA256)
			if err != nil {
				return nil, err
			}
			if rsa.VerifyPKCS1v15(carried, crypto.SHA256, h[:], sig) != nil {
				return nil, errors.New("the PCR key does not match the one the kernel image carries")
			}
			e.Signature = base64.StdEncoding.EncodeToString(sig)
		}
	}
	return json.Marshal(p)
}
