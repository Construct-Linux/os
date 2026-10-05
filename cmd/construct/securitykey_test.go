package main

import (
	"bufio"
	"io"
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
	keys, err := securityKeysFrom([]string{"/dev/hidraw4", "/dev/hidraw5"}, pinFile(t, "482913\r\nbackup-pin\n"), nil)
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
			_, err := securityKeysFrom(c.devices, file, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	if keys, err := securityKeysFrom(nil, "", nil); err != nil || keys != nil {
		t.Errorf("no key: %v, %v", keys, err)
	}
}

// Through pkexec the installer has one pipe: the password's line, then each key's PIN.
func TestSecurityKeysPINsAfterThePassword(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("correct horse\n482913\nbackup-pin\n"))
	who, err := personFrom(in, io.Discard, "ada", "", "-")
	if err != nil || who.password != "correct horse" {
		t.Fatalf("%q %v", who.password, err)
	}
	keys, err := securityKeysFrom([]string{"/dev/hidraw4", "/dev/hidraw5"}, "-", in)
	if err != nil || len(keys) != 2 || keys[0].pin != "482913" || keys[1].pin != "backup-pin" {
		t.Errorf("%+v %v", keys, err)
	}
}

// A new key says noclientPin among its options; one with a PIN says clientPin.
func TestNoClientPIN(t *testing.T) {
	fresh := "proto: 0x02\nmajor: 0x05\noptions: rk, up, noplat, noclientPin, credentialMgmtPreview\n"
	set := "options: rk, up, noplat, clientPin, credentialMgmtPreview\n"
	if !noClientPIN(fresh) || noClientPIN(set) || noClientPIN("aaguid: 00\n") {
		t.Error(noClientPIN(fresh), noClientPIN(set))
	}
}
