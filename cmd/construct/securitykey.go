package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// securityKey is a FIDO2 key (a YubiKey) that opens /var at boot with its PIN and a touch, beside
// the person's password: its hidraw node, and its PIN.
type securityKey struct {
	device, pin string
}

// securityKeysFrom pairs the keys named on the command line with their PINs, one a line of
// pinFile in the same order - a file, or - for the lines in after the password, which is how the
// graphical installer gives them through pkexec - so no PIN is an argument another process could
// read.
func securityKeysFrom(devices []string, pinFile string, in *bufio.Reader) ([]securityKey, error) {
	if len(devices) == 0 {
		if pinFile != "" {
			return nil, fmt.Errorf("--key-pin-file without a --security-key")
		}
		return nil, nil
	}
	if pinFile == "" {
		return nil, fmt.Errorf("--security-key needs --key-pin-file: its PIN, one a line")
	}
	var r io.Reader = in
	if pinFile != "-" {
		f, err := os.Open(pinFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	var pins []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		pins = append(pins, strings.TrimRight(sc.Text(), "\r"))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(pins) != len(devices) {
		return nil, fmt.Errorf("%d security keys and %d PINs", len(devices), len(pins))
	}
	keys := make([]securityKey, len(devices))
	for i, d := range devices {
		// A FIDO2 PIN is 4 to 63 bytes (CTAP 2.1): a shorter one the key itself refuses, after
		// the touch it asked for.
		if n := len(pins[i]); n < 4 || n > 63 {
			return nil, fmt.Errorf("the PIN of %s is %d bytes; a key's PIN is 4 to 63", d, n)
		}
		keys[i] = securityKey{device: d, pin: pins[i]}
	}
	return keys, nil
}

// enrollSecurityKeys adds a slot to /var's LUKS2 header for each key, opened by the key's PIN and
// a touch (systemd-cryptenroll's FIDO2 token, which the initrd's var-key reads), unlocked for it
// by the person's password, and makes each key's pam_u2f credential for owner: what construct key
// records. Each asks to be touched twice: the log says which, for the installer to show.
func enrollSecurityKeys(ctx context.Context, log *slog.Logger, partition, password, owner string, keys []securityKey) ([]registeredKey, error) {
	var done []registeredKey
	for i, k := range keys {
		log.Info("touch the security key", "key", i+1, "of", len(keys), "device", k.device)
		before, err := fido2Slots(ctx, partition)
		if err != nil {
			return nil, err
		}
		if err := enrollSecurityKey(ctx, partition, password, k); err != nil {
			return nil, fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
		after, err := fido2Slots(ctx, partition)
		if err != nil {
			return nil, err
		}
		slot, err := newSlot(before, after)
		if err != nil {
			return nil, fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
		log.Info("touch the security key again", "key", i+1, "of", len(keys), "device", k.device)
		u2f, err := u2fCredential(ctx, k.device, owner, k.pin)
		if err != nil {
			return nil, fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
		name := "Security key"
		if i > 0 {
			name = fmt.Sprintf("Backup key %d", i)
		}
		done = append(done, registeredKey{Name: name, User: owner, Slot: slot, U2F: u2f, Added: time.Now().UTC().Format(time.DateOnly)})
	}
	return done, nil
}

// enrollSecurityKey enrolls one key. systemd-cryptenroll takes the password from $PASSWORD but a
// FIDO2 PIN only as the credential cryptenroll.fido2-pin (it asks a terminal otherwise): a
// credentials directory of the one file, root's alone and gone once enrolled.
func enrollSecurityKey(ctx context.Context, partition, password string, k securityKey) error {
	if err := ensurePIN(ctx, k); err != nil {
		return err
	}
	creds, err := os.MkdirTemp("/run", "construct-key-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(creds)
	if err := os.WriteFile(filepath.Join(creds, "cryptenroll.fido2-pin"), []byte(k.pin), 0o600); err != nil {
		return err
	}
	return cmd(ctx, nil, []string{"PASSWORD=" + password, "CREDENTIALS_DIRECTORY=" + creds},
		"systemd-cryptenroll", "--fido2-device="+k.device, "--fido2-with-client-pin=yes",
		"--fido2-with-user-presence=yes", partition)
}

// ensurePIN gives k the PIN it is to be enrolled with when it has none yet - a new key - since the
// disk opens with the key's PIN and a touch. A key that has one keeps it: a wrong one fails the
// enrollment, as it would fail at boot.
func ensurePIN(ctx context.Context, k securityKey) error {
	var out bytes.Buffer
	if err := cmdOut(ctx, nil, nil, &out, "fido2-token", "-I", k.device); err != nil {
		return err
	}
	if !noClientPIN(out.String()) {
		return nil
	}
	return withPIN(ctx, k.pin, "fido2-token", "-S", k.device)
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
