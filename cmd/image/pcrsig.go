package main

import (
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
)

// The kernel image's PCR 11 policy, signed offline. An installed machine's /var is sealed in the
// TPM to whatever PCR 11 policy the PCR key signed (cmd/drift makeVar), so a kernel image this
// key did not sign never opens it. The key is RSA-2048 - the one exception to Ed25519: a TPM cannot
// verify Ed25519, and systemd verifies a PCR policy's signature as RSA only (RSASSA, SHA-256) - and
// like the release key it never enters a container: ukify computes the
// policy's digests in build/tools' container (image usb), the host signs them here (image
// sign-pcr), and ukify joins the signature to the kernel image (image usb-join).

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
	keyFile := fl.String("key", "", "the PCR key: RSA-2048, PEM")
	_ = fl.Parse(args)
	stage := filepath.Join(out, "usb")
	key, err := readPCRKey(*keyFile)
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

// readPCRKey reads the PCR key, refusing anything but RSA-2048.
func readPCRKey(file string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM", file)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok || r.N.BitLen() != 2048 {
		return nil, fmt.Errorf("%s is not an RSA-2048 key", file)
	}
	return r, nil
}

// signPolicy signs every boot phase's policy in every bank, as ukify does by default: systemd
// seals its own secrets to the signed policy too (systemd-tpm2-setup's NvPCR anchor, made in the
// initrd and opened in the root), so a phase left unsigned breaks them there. /var's key still
// needs the person's PIN in any phase. systemd checks a PKCS#1 v1.5 signature of the policy's
// SHA-256; each is checked here against pub, the key the kernel image carries, which a wrong key
// would fail.
func signPolicy(digests []byte, key *rsa.PrivateKey, pub []byte) ([]byte, error) {
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
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
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
