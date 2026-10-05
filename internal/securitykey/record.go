// Package securitykey is a person's FIDO2 security keys on an installed machine. A key does two
// things there: it opens /var at boot with its PIN and a touch (a systemd-fido2 token in the LUKS2
// header, luks.go), and it signs its person in where sudo, polkit and the lock screen ask, with a
// touch (a pam_u2f credential made on the key, fido.go). The record (this file) names each key and
// holds both of its credentials, so a key can be listed and removed whole; pam_u2f's mappings are
// written from it.
package securitykey

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/fsx"
)

const (
	// RecordFile, under /etc, is the record: what construct key lists.
	RecordFile = "construct/security-keys.json"
	// MappingsFile, under /etc, is pam_u2f's central file (etc/pam.d/construct-key): one, written
	// by root alone - a key a session could register for itself would be a password anything in
	// that session could set.
	MappingsFile = "u2f_mappings"
	// lockFile, under /etc, exists while unplugging a key is to lock the screen.
	lockFile = "construct/lock-on-key-removal"
)

// Key is one security key on this machine.
type Key struct {
	Name string `json:"name"`
	User string `json:"user"`
	// Slot is the LUKS2 keyslot it opens /var with, or -1 when /var is not encrypted (a stick).
	Slot int `json:"slot"`
	// U2F is its pam_u2f credential: key handle, public key, COSE type and options.
	U2F   string `json:"u2f"`
	Added string `json:"added"`
}

// Read reads the keys recorded under etc: none when nothing was ever recorded.
func Read(etc string) ([]Key, error) {
	b, err := os.ReadFile(filepath.Join(etc, RecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Keys []Key `json:"keys"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", RecordFile, err)
	}
	return f.Keys, nil
}

// Write writes the record and pam_u2f's mappings from it under etc, each whole or not at all.
func Write(etc string, keys []Key) error {
	b, err := json.MarshalIndent(struct {
		Keys []Key `json:"keys"`
	}{keys}, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFile(filepath.Join(etc, RecordFile), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return fsx.WriteFile(filepath.Join(etc, MappingsFile), []byte(Mappings(keys)), 0o644)
}

// Mappings is pam_u2f's central file: a line an account, its credentials after its name.
func Mappings(keys []Key) string {
	byUser := map[string][]string{}
	for _, k := range keys {
		if k.U2F != "" {
			byUser[k.User] = append(byUser[k.User], k.U2F)
		}
	}
	users := make([]string, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	sort.Strings(users)
	var b strings.Builder
	for _, u := range users {
		b.WriteString(u + ":" + strings.Join(byUser[u], ":") + "\n")
	}
	return b.String()
}

// LocksOnRemoval says whether unplugging a key is to lock the sessions: the person walks away with
// the key, and the screen locks behind them. Off unless they turned it on.
func LocksOnRemoval(etc string) (bool, error) {
	_, err := os.Stat(filepath.Join(etc, lockFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// SetLockOnRemoval turns locking on unplugging on or off.
func SetLockOnRemoval(etc string, on bool) error {
	path := filepath.Join(etc, lockFile)
	if on {
		return fsx.WriteFile(path, nil, 0o644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
