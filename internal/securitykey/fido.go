package securitykey

import (
	"github.com/Construct-Linux/os/internal/program"

	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// origin is pam_u2f's origin and appid (etc/pam.d/construct-key): fixed, so renaming the machine
// does not orphan its keys.
const origin = "pam://construct"

// Devices are the hidraw nodes of the FIDO2 keys plugged in, as fido2-token lists them.
func Devices(ctx context.Context) ([]string, error) {
	var out bytes.Buffer
	if err := (program.With{Stdout: &out}).Run(ctx, "fido2-token", "-L"); err != nil {
		return nil, err
	}
	var devs []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if dev, _, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(dev, "/dev/") {
			devs = append(devs, dev)
		}
	}
	return devs, nil
}

// Credential makes a pam_u2f credential for user on the key at device: fido2-cred makes it, on
// that key and no other - pamu2fcfg takes the first key it finds, and a person adding a backup may
// have both plugged in - and verifies it, and the line is pamu2fcfg's. The key asks for a touch,
// and for its PIN when it makes no credential without one.
func Credential(ctx context.Context, device, user, pin string) (string, error) {
	dir, err := os.MkdirTemp("/run", "construct-u2f-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cdh, uid := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(cdh); err != nil {
		return "", err
	}
	if _, err := rand.Read(uid); err != nil {
		return "", err
	}
	in, made, verified := filepath.Join(dir, "in"), filepath.Join(dir, "made"), filepath.Join(dir, "verified")
	b64 := base64.StdEncoding.EncodeToString
	if err := os.WriteFile(in, []byte(b64(cdh)+"\n"+origin+"\n"+user+"\n"+b64(uid)+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := withPIN(ctx, pin, "fido2-cred", "-M", "-i", in, "-o", made, device); err != nil {
		return "", notTouched(err)
	}
	if err := program.Run(ctx, "fido2-cred", "-V", "-i", made, "-o", verified); err != nil {
		return "", err
	}
	out, err := os.ReadFile(verified)
	if err != nil {
		return "", err
	}
	return credentialLine(out)
}

// credentialLine turns fido2-cred -V's output - the credential's id in base64, then its public key
// in PEM - into pam_u2f's form: the id, the raw P-256 point x||y in base64, es256, and +presence (a
// touch, no PIN: pam_u2f's own default).
func credentialLine(verified []byte) (string, error) {
	id, rest, ok := bytes.Cut(verified, []byte("\n"))
	if !ok || len(bytes.TrimSpace(id)) == 0 {
		return "", errors.New("fido2-cred wrote no credential id")
	}
	if _, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(id))); err != nil {
		return "", fmt.Errorf("the credential id: %w", err)
	}
	block, _ := pem.Decode(rest)
	if block == nil {
		return "", errors.New("fido2-cred wrote no public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("a %T key: pam_u2f is given an es256 one", pub)
	}
	point, err := ec.Bytes()
	if err != nil {
		return "", err
	}
	// Bytes is the uncompressed point, 0x04||x||y; pam_u2f's key is x||y, as pamu2fcfg writes it.
	if len(point) != 65 || point[0] != 4 {
		return "", fmt.Errorf("a %d-byte point: es256's is 65", len(point))
	}
	return string(bytes.TrimSpace(id)) + "," + base64.StdEncoding.EncodeToString(point[1:]) + ",es256,+presence", nil
}

// ensurePIN gives the key at device pin when it has none yet - a new key - since the disk opens
// with the key's PIN and a touch. A key that has one keeps it: a wrong one fails the enrollment,
// as it would fail at boot.
func ensurePIN(ctx context.Context, device, pin string) error {
	var out bytes.Buffer
	if err := (program.With{Stdout: &out}).Run(ctx, "fido2-token", "-I", device); err != nil {
		return err
	}
	if !noClientPIN(out.String()) {
		return nil
	}
	return withPIN(ctx, pin, "fido2-token", "-S", device)
}

// noClientPIN says from fido2-token -I's output whether the key has no PIN set: its options list
// clientPin when one is, noclientPin when none is (and neither when it cannot have one).
func noClientPIN(info string) bool {
	for _, line := range strings.Split(info, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "options" {
			for _, o := range strings.Split(v, ",") {
				if strings.TrimSpace(o) == "noclientPin" {
					return true
				}
			}
		}
	}
	return false
}

// withPIN runs a libfido2 tool that may ask for the key's PIN: they read it from a terminal and
// drop what was typed before they asked, so the tool gets one (script) and the PIN is typed each
// time a question appears - never an argument, which another process could read.
func withPIN(ctx context.Context, pin, name string, args ...string) error {
	quoted := []string{name}
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	c := exec.CommandContext(ctx, "script", "-qec", strings.Join(quoted, " "), "/dev/null")
	stdin, err := c.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	var said bytes.Buffer
	buf := make([]byte, 512)
	answered := 0
	for {
		n, rerr := stdout.Read(buf)
		said.Write(buf[:n])
		// Each question about the PIN gets it once: setting a new one asks twice.
		for ; answered < bytes.Count(said.Bytes(), []byte("PIN")); answered++ {
			if pin == "" {
				_ = c.Process.Kill()
				_ = c.Wait()
				return fmt.Errorf("%s: the key asks for its PIN, and none was given", name)
			}
			_, _ = io.WriteString(stdin, pin+"\n")
		}
		if rerr != nil {
			break
		}
	}
	_ = stdin.Close()
	if err := c.Wait(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(strings.ReplaceAll(said.String(), pin, "")))
	}
	return nil
}

// ErrNotTouched is a key that waited for its touch and got none: libfido2's
// FIDO_ERR_ACTION_TIMEOUT, or FIDO_ERR_OPERATION_DENIED, which a YubiKey answers when its wait runs
// out. Nothing was made on the key or the disk, and asking again is safe.
var ErrNotTouched = errors.New("the security key was not touched in time")

// notTouched marks err as ErrNotTouched when the key said so.
func notTouched(err error) error {
	if err != nil && (strings.Contains(err.Error(), "FIDO_ERR_ACTION_TIMEOUT") || strings.Contains(err.Error(), "FIDO_ERR_OPERATION_DENIED")) {
		return fmt.Errorf("%w: %w", ErrNotTouched, err)
	}
	return err
}
