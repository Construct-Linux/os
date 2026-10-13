package securitykey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// VarPartition is the encrypted partition the running /var is opened from, or "" when /var is not
// encrypted (a stick).
func VarPartition(ctx context.Context) (string, error) {
	if _, err := os.Stat("/dev/mapper/var"); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	var out bytes.Buffer
	if err := run(ctx, nil, &out, "cryptsetup", "status", "var"); err != nil {
		return "", err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "device" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("cryptsetup status var names no device")
}

// Enroll adds a slot to partition's LUKS2 header that the key at device opens with pin and a touch
// (systemd-cryptenroll's FIDO2 token, which the initrd's var-key reads), unlocked for it by the
// person's password, and answers the slot. A new key is given pin first. It asks to be touched.
func Enroll(ctx context.Context, partition, password, device, pin string) (int, error) {
	if err := ensurePIN(ctx, device, pin); err != nil {
		return 0, err
	}
	before, err := tokenSlots(ctx, partition)
	if err != nil {
		return 0, err
	}
	// systemd-cryptenroll takes the password from $PASSWORD but a FIDO2 PIN only as the credential
	// cryptenroll.fido2-pin (it asks a terminal otherwise): a credentials directory of the one
	// file, root's alone and gone once enrolled.
	creds, err := os.MkdirTemp("/run", "construct-key-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(creds)
	if err := os.WriteFile(filepath.Join(creds, "cryptenroll.fido2-pin"), []byte(pin), 0o600); err != nil {
		return 0, err
	}
	if err := run(ctx, []string{"PASSWORD=" + password, "CREDENTIALS_DIRECTORY=" + creds}, nil,
		"systemd-cryptenroll", "--fido2-device="+device, "--fido2-with-client-pin=yes",
		"--fido2-with-user-presence=yes", partition); err != nil {
		return 0, notTouched(err)
	}
	after, err := tokenSlots(ctx, partition)
	if err != nil {
		return 0, err
	}
	return newSlot(before, after)
}

// Wipe removes slot from partition's header, only when a security key's token opens it: a slot
// the record names is never the password's.
func Wipe(ctx context.Context, partition, password string, slot int) error {
	slots, err := tokenSlots(ctx, partition)
	if err != nil {
		return err
	}
	if !slices.Contains(slots, slot) {
		return nil
	}
	return run(ctx, []string{"PASSWORD=" + password}, nil, "systemd-cryptenroll", "--wipe-slot="+strconv.Itoa(slot), partition)
}

// Header is a LUKS2 header by kinds: its keyslots, its tokens and the slots each token opens.
// Nothing in it is secret - no salt, digest or wrapped key is read.
type Header struct {
	Keyslots map[string]Keyslot `json:"keyslots"`
	Tokens   map[string]Token   `json:"tokens"`
}

// Keyslot is a slot's kind (luks2).
type Keyslot struct {
	Type string `json:"type"`
}

// Token is what opens slots without the password: systemd-tpm2, systemd-fido2, systemd-recovery.
type Token struct {
	Type     string   `json:"type"`
	Keyslots []string `json:"keyslots"`
}

// ReadHeader reads partition's LUKS2 header.
func ReadHeader(ctx context.Context, partition string) (Header, error) {
	var out bytes.Buffer
	if err := run(ctx, nil, &out, "cryptsetup", "luksDump", "--dump-json-metadata", partition); err != nil {
		return Header{}, err
	}
	return parseHeader(out.Bytes())
}

func parseHeader(b []byte) (Header, error) {
	var h Header
	err := json.Unmarshal(b, &h)
	return h, err
}

// tokenSlots are the LUKS2 keyslots of partition that a systemd-fido2 token opens.
func tokenSlots(ctx context.Context, partition string) ([]int, error) {
	h, err := ReadHeader(ctx, partition)
	if err != nil {
		return nil, err
	}
	return h.FIDO2Slots()
}

// FIDO2Slots are the keyslots a systemd-fido2 token opens, in order.
func (h Header) FIDO2Slots() ([]int, error) {
	var slots []int
	for _, t := range h.Tokens {
		if t.Type != "systemd-fido2" {
			continue
		}
		for _, s := range t.Keyslots {
			n, err := strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("keyslot %q: %w", s, err)
			}
			slots = append(slots, n)
		}
	}
	slices.Sort(slots)
	return slots, nil
}

// newSlot is the one slot in after that was not in before.
func newSlot(before, after []int) (int, error) {
	var added []int
	for _, s := range after {
		if !slices.Contains(before, s) {
			added = append(added, s)
		}
	}
	if len(added) != 1 {
		return 0, fmt.Errorf("%d new security key slots, want 1", len(added))
	}
	return added[0], nil
}
