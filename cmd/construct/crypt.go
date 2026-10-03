package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	if err := cmd(ctx, pw, nil, "cryptsetup", "luksFormat", "--batch-mode", "--type", "luks2",
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
		if err := cmdOut(ctx, nil, []string{"PASSWORD=" + p.password, "NEWPIN=" + p.password}, nil,
			"systemd-cryptenroll", "--tpm2-device=auto", "--tpm2-with-pin=yes",
			"--tpm2-public-key="+pcrPublicKey, "--tpm2-public-key-pcrs=11", "--tpm2-pcrs=", partition); err != nil {
			return "", err
		}
	}
	var key bytes.Buffer
	if err := cmdOut(ctx, nil, []string{"PASSWORD=" + p.password}, &key,
		"systemd-cryptenroll", "--recovery-key", partition); err != nil {
		return "", err
	}
	recovery := strings.TrimSpace(key.String())
	if recovery == "" {
		return "", errors.New("systemd-cryptenroll made no recovery key")
	}
	if err := cmd(ctx, pw, nil, "cryptsetup", "open", "--key-file", "-", partition, installVar); err != nil {
		return "", err
	}
	dev := "/dev/mapper/" + installVar
	err := cmd(ctx, nil, nil, "mkfs.ext4", "-q", "-L", "var", dev)
	if err == nil {
		log.Info("preparing /var")
		err = prepareVar(ctx, dev, p)
	}
	if cerr := cmd(ctx, nil, nil, "cryptsetup", "close", installVar); err == nil {
		err = cerr
	}
	return recovery, err
}

// cmd runs a program with stdin as its input and env added to construct's, its output on construct's
// standard error.
func cmd(ctx context.Context, stdin []byte, env []string, name string, args ...string) error {
	return cmdOut(ctx, stdin, env, nil, name, args...)
}

// cmdOut is cmd with the program's standard output in out. The error names the program and its
// arguments, never env, which holds the password.
func cmdOut(ctx context.Context, stdin []byte, env []string, out *bytes.Buffer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if out != nil {
		c.Stdout = out
	}
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
