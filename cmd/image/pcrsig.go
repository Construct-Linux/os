package main

import (
	"context"
	"flag"
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

// signPCR signs the digests the last image usb left in its stage with the key in args.
func signPCR(args []string) error {
	fl := flag.NewFlagSet("sign-pcr", flag.ExitOnError)
	ref := fl.String("key", "", "the PCR key, RSA-2048: a PEM file, or kms:<key>")
	_ = fl.Parse(args)
	stage := filepath.Join(out, "usb")
	b, err := readBuilt(stage)
	if err != nil {
		return err
	}
	if err := refuseKMS(b, *ref); err != nil {
		return err
	}
	key, err := sign.OpenRSA(context.Background(), *ref)
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
	signed, err := sign.PCRPolicy(digests, key, pub)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, pcrSignature), signed, 0o644)
}
