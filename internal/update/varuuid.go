package update

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	app, err := hex.DecodeString(strings.ReplaceAll(TypeVar, "-", ""))
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write(app)
	id := m.Sum(nil)[:16]
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	h := hex.EncodeToString(id)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
