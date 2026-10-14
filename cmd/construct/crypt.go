package main

import (
	"github.com/Construct-Linux/os/internal/program"

	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// An installed machine's /var is LUKS2: everything of the person's is on it (home, the /etc
// overlay, Docker, Flatpaks). systemd-cryptsetup opens it in the initrd (image/initrd's crypttab,
// pulled in by its udev rule once the partition appears) by the person's password: typed as the
// TPM's PIN where there is a TPM - the TPM then refuses a kernel image Construct's PCR key did not
// sign, and counts wrong PINs against dictionary attacks - or as the passphrase where there is none.

// pcrPublicKey is the key the running kernel image's PCR 11 policy is signed with: systemd-stub
// hands it to the initrd, which copies it here. A stick's kernel image is the release's, so the
// TPM is sealed to every kernel image signed with it, the updates' too.
const pcrPublicKey = "/run/systemd/tpm2-pcr-public-key.pem"

// installVar is the name /var is opened under while it is made: not "var", which udev would give
// the live system's by-designator name.
const installVar = "construct-install-var"

// hasTPM2 says whether the machine has a TPM 2.0.
func hasTPM2(sys string) bool {
	b, err := os.ReadFile(sys + "/class/tpm/tpm0/tpm_version_major")
	return err == nil && strings.TrimSpace(string(b)) == "2"
}

// makeVar makes /var on partition for p, encrypted, and answers its recovery key: the one secret
// besides the password that opens it, shown once.
func makeVar(ctx context.Context, log *slog.Logger, partition string, p person, tpm bool) (string, error) {
	pw := []byte(p.password)
	if err := (program.With{Stdin: pw}).Run(ctx, "cryptsetup", "luksFormat", "--batch-mode", "--type", "luks2",
		"--label", "var", "--key-file", "-", partition); err != nil {
		return "", err
	}
	if tpm {
		log.Info("enrolling the TPM")
		if _, err := os.Stat(pcrPublicKey); err != nil {
			return "", fmt.Errorf("the TPM: the running kernel image carries no PCR key: %w", err)
		}
		// PCR 11 alone, through the signed policy: the kernel image, its initrd and command line as
		// measured - an update's image is signed by the same key.
		if err := (program.With{Env: []string{"PASSWORD=" + p.password, "NEWPIN=" + p.password}}).Run(ctx,
			"systemd-cryptenroll", "--tpm2-device=auto", "--tpm2-with-pin=yes",
			"--tpm2-public-key="+pcrPublicKey, "--tpm2-public-key-pcrs=11", "--tpm2-pcrs=", partition); err != nil {
			return "", err
		}
	}
	registered, err := enrollSecurityKeys(ctx, log, partition, p.password, p.user.Name, p.keys)
	if err != nil {
		return "", err
	}
	var key bytes.Buffer
	if err := (program.With{Env: []string{"PASSWORD=" + p.password}, Stdout: &key}).Run(ctx,
		"systemd-cryptenroll", "--recovery-key", partition); err != nil {
		return "", err
	}
	recovery := strings.TrimSpace(key.String())
	if recovery == "" {
		return "", errors.New("systemd-cryptenroll made no recovery key")
	}
	if err := (program.With{Stdin: pw}).Run(ctx, "cryptsetup", "open", "--key-file", "-", partition, installVar); err != nil {
		return "", err
	}
	dev := "/dev/mapper/" + installVar
	// verity: containerd checks its content store with fs-verity, and ext4 takes it only from a
	// file system made with the feature. A factory reset and a stick's first boot make /var so too.
	err = program.Run(ctx, "mkfs.ext4", "-q", "-O", "verity", "-L", "var", dev)
	if err == nil {
		log.Info("preparing /var")
		err = prepareVar(ctx, dev, p, registered)
	}
	if cerr := program.Run(ctx, "cryptsetup", "close", installVar); err == nil {
		err = cerr
	}
	return recovery, err
}
