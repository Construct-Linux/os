package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A key and its backup, each with its own PIN, in the order they were named.
func TestSecurityKeysPairWithTheirPINs(t *testing.T) {
	keys, err := securityKeysFrom([]string{"/dev/hidraw4", "/dev/hidraw5"}, pinFile(t, "482913\r\nbackup-pin\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []securityKey{{"/dev/hidraw4", "482913"}, {"/dev/hidraw5", "backup-pin"}}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("%+v", keys)
	}
}

func TestSecurityKeysRefuse(t *testing.T) {
	for name, c := range map[string]struct {
		devices []string
		pins    string
		want    string
	}{
		"a PIN short of a key's four": {[]string{"/dev/hidraw4"}, "123\n", "4 to 63"},
		"fewer PINs than keys":        {[]string{"/dev/hidraw4", "/dev/hidraw5"}, "482913\n", "2 security keys and 1 PINs"},
		"a key without its PIN file":  {[]string{"/dev/hidraw4"}, "", "--key-pin-file"},
	} {
		t.Run(name, func(t *testing.T) {
			file := ""
			if c.pins != "" {
				file = pinFile(t, c.pins)
			}
			_, err := securityKeysFrom(c.devices, file)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	if keys, err := securityKeysFrom(nil, ""); err != nil || keys != nil {
		t.Errorf("no key: %v, %v", keys, err)
	}
}
