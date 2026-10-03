package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// securityKey is a FIDO2 key (a YubiKey) that opens /var at boot with its PIN and a touch, beside
// the person's password: its hidraw node, and its PIN.
type securityKey struct {
	device, pin string
}

// securityKeysFrom pairs the keys named on the command line with their PINs, one a line of
// pinFile in the same order - a file, or /dev/fd/N from the graphical installer, so no PIN is an
// argument another process could read.
func securityKeysFrom(devices []string, pinFile string) ([]securityKey, error) {
	if len(devices) == 0 {
		if pinFile != "" {
			return nil, fmt.Errorf("--key-pin-file without a --security-key")
		}
		return nil, nil
	}
	if pinFile == "" {
		return nil, fmt.Errorf("--security-key needs --key-pin-file: its PIN, one a line")
	}
	f, err := os.Open(pinFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var pins []string
	sc := bufio.NewScanner(f)
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
// by the person's password. Each asks to be touched: the log says which, for the installer to show.
func enrollSecurityKeys(ctx context.Context, log *slog.Logger, partition, password string, keys []securityKey) error {
	for i, k := range keys {
		log.Info("touch the security key", "key", i+1, "of", len(keys), "device", k.device)
		if err := enrollSecurityKey(ctx, partition, password, k); err != nil {
			return fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
	}
	return nil
}

// enrollSecurityKey enrolls one key. systemd-cryptenroll takes the password from $PASSWORD but a
// FIDO2 PIN only as the credential cryptenroll.fido2-pin (it asks a terminal otherwise): a
// credentials directory of the one file, root's alone and gone once enrolled.
func enrollSecurityKey(ctx context.Context, partition, password string, k securityKey) error {
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
