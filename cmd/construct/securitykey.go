package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/securitykey"
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

// enrollSecurityKeys enrolls each key with the new disk (securitykey.Enroll: its PIN and a touch
// open /var, unlocked for it by the person's password) and makes its pam_u2f credential for owner:
// what construct key records. Each asks to be touched twice: the log says which, and which try, for
// the installer to show. A touch missed is asked for again, up to touchTries times: by then the
// disk is partitioned and its TPM enrolled, and starting the install over costs minutes. Any
// other failure fails the install, whose disk is then never booted: no slot is taken back.
func enrollSecurityKeys(ctx context.Context, log *slog.Logger, partition, password, owner string, keys []securityKey) ([]securitykey.Key, error) {
	var done []securitykey.Key
	for i, k := range keys {
		var slot int
		err := untilTouched(func(try int) (err error) {
			log.Info("touch the security key", "key", i+1, "of", len(keys), "device", k.device, "try", try)
			slot, err = securitykey.Enroll(ctx, partition, password, k.device, k.pin)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
		var u2f string
		err = untilTouched(func(try int) (err error) {
			log.Info("touch the security key again", "key", i+1, "of", len(keys), "device", k.device, "try", try)
			u2f, err = securitykey.Credential(ctx, k.device, owner, k.pin)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("security key %d (%s): %w", i+1, k.device, err)
		}
		name := "Security key"
		if i > 0 {
			name = fmt.Sprintf("Backup key %d", i)
		}
		done = append(done, securitykey.Key{Name: name, User: owner, Slot: slot, U2F: u2f, Added: time.Now().UTC().Format(time.DateOnly)})
	}
	return done, nil
}

// touchTries is how many times a touch is asked for: a key waits about 30 s for each.
const touchTries = 3

// touchPrompt is what a person reads before a touch: prompt, said again after a missed one.
func touchPrompt(try int, prompt string) string {
	if try == 1 {
		return prompt
	}
	return "not touched in time; " + prompt
}

// untilTouched runs ask, numbering its tries from 1, again while the key was not touched. After
// the last miss the error is said for a person - the installer shows it as it is - and the tools'
// own words stay in the log, where each try wrote them.
func untilTouched(ask func(try int) error) error {
	for try := 1; try <= touchTries; try++ {
		if err := ask(try); !errors.Is(err, securitykey.ErrNotTouched) {
			return err
		}
	}
	return fmt.Errorf("%w, %d times: touch it when it blinks", securitykey.ErrNotTouched, touchTries)
}
