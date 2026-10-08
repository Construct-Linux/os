package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/securitykey"
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

// A missed touch is asked for again, up to touchTries times; any other failure, or a touch, ends
// the asking.
func TestUntilTouched(t *testing.T) {
	missed := fmt.Errorf("enroll: %w", securitykey.ErrNotTouched)
	other := errors.New("cryptsetup: no such device")
	for _, c := range []struct {
		name    string
		answers []error
		tries   int
		want    error
	}{
		{"touched", []error{nil}, 1, nil},
		{"touched on the second try", []error{missed, nil}, 2, nil},
		{"never touched", []error{missed, missed, missed, nil}, touchTries, securitykey.ErrNotTouched},
		{"another failure", []error{other, nil}, 1, other},
	} {
		tries := 0
		err := untilTouched(func(try int) error {
			tries++
			if try != tries {
				t.Errorf("%s: try %d, want %d", c.name, try, tries)
			}
			return c.answers[try-1]
		})
		if tries != c.tries || !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
			t.Errorf("%s: %d tries, err %v; want %d, %v", c.name, tries, err, c.tries, c.want)
		}
		// The installer shows the error as it is: a person's words, not the last tool's.
		if c.want == securitykey.ErrNotTouched && strings.Contains(err.Error(), "enroll") {
			t.Errorf("%s: %q carries the tool's words", c.name, err)
		}
	}
}
