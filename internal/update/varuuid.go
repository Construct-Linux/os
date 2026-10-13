package update

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VarUUID is the partition UUID /var must have for machineID (as /etc/machine-id holds it):
// systemd's machine-app-specific id of /var's partition type (sd_id128_get_machine_app_specific -
// HMAC-SHA256 keyed by the machine id, made a v4 UUID). The Discoverable Partitions Specification
// asks it so a /var is mounted only by the installation it belongs to; the dissect_image udev
// builtin ignores a var partition whose UUID is anything else.
func VarUUID(machineID string) (string, error) {
	key, err := hex.DecodeString(strings.TrimSpace(machineID))
	if err != nil || len(key) != 16 {
		return "", fmt.Errorf("update: %q is not a machine id", strings.TrimSpace(machineID))
	}
	app, err := hex.DecodeString(strings.ReplaceAll(typeVar, "-", ""))
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write(app)
	id := m.Sum(nil)[:16]
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return formatUUID(hex.EncodeToString(id)), nil
}

// FirmwareID checks that the machine has an id of its own to bind /var to: the image boots with
// systemd.machine_id=firmware, which takes the firmware's product UUID, and on a board without
// a usable one (none, all zeros, all ones) gives every boot a random id - an installed /var would
// be found by no boot after the first.
func FirmwareID(sys string) error {
	b, err := os.ReadFile(filepath.Join(sys, "class", "dmi", "id", "product_uuid"))
	if err != nil {
		return fmt.Errorf("install: the firmware gives this machine no UUID to bind /var to: %w", err)
	}
	id := strings.ReplaceAll(strings.TrimSpace(string(b)), "-", "")
	if len(id) != 32 || strings.Trim(id, "0") == "" || strings.Trim(strings.ToLower(id), "f") == "" {
		return fmt.Errorf("install: the firmware's UUID %q is no machine's own: /var could not be bound to it", strings.TrimSpace(string(b)))
	}
	return nil
}
