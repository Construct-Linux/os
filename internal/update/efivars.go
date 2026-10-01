package update

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/unix"
)

// EFIVars is where efivarfs is mounted: a file per firmware variable, named <name>-<vendor GUID>,
// holding its attributes - four bytes, little-endian - and its value. The installer writes the
// boot entry through it; a boot check reads which kernel image systemd-boot is counting.
const EFIVars = "/sys/firmware/efi/efivars"

// The vendors whose variables are read and written: UEFI's global ones (Boot####, BootOrder) and
// systemd-boot's.
const (
	Global = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	Loader = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
)

// Attributes a variable is written with; a boot entry's is NVBootRuntime: non-volatile, and seen
// at boot and after.
const (
	NonVolatile   = 0x01
	BootService   = 0x02
	Runtime       = 0x04
	NVBootRuntime = NonVolatile | BootService | Runtime
)

// fsImmutable is the inode flag (FS_IMMUTABLE_FL) efivarfs sets on a variable's file.
const fsImmutable = 0x10

const attributesSize = 4

// Vars is the variables under a directory: EFIVars, or a test's.
type Vars string

func (v Vars) path(name, guid string) string { return filepath.Join(string(v), name+"-"+guid) }

// Read is the value of the variable name of vendor guid, without its attributes. A variable the
// firmware does not have is an error that is fs.ErrNotExist.
func (v Vars) Read(name, guid string) ([]byte, error) {
	raw, err := os.ReadFile(v.path(name, guid))
	if err != nil {
		return nil, err
	}
	if len(raw) < attributesSize {
		return nil, fmt.Errorf("efivars: %s has no attributes", name)
	}
	return raw[attributesSize:], nil
}

// Write sets the variable name of vendor guid to value, with attrs, in one write: efivarfs takes
// one write as one SetVariable call, the firmware's to accept or refuse.
func (v Vars) Write(name, guid string, attrs uint32, value []byte) error {
	p := v.path(name, guid)
	// efivarfs makes a variable's file immutable so that nothing removes a variable by accident;
	// a new value is not an accident. A file system without the flag has nothing to unset.
	if _, err := os.Stat(p); err == nil {
		if err := unsetImmutable(p); err != nil {
			return fmt.Errorf("efivars: %s: %w", name, err)
		}
	}
	body := binary.LittleEndian.AppendUint32(nil, attrs)
	if err := os.WriteFile(p, append(body, value...), 0o644); err != nil {
		return fmt.Errorf("efivars: setting %s: %w", name, err)
	}
	return nil
}

// unsetImmutable clears the immutable flag of the file at p, where its file system has one.
func unsetImmutable(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only
	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
		return nil
	}
	if err != nil || flags&fsImmutable == 0 {
		return err
	}
	return unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, int(flags&^fsImmutable))
}

// String is a variable that holds a NUL-terminated UTF-16 string, as systemd-boot writes them.
func (v Vars) String(name, guid string) (string, error) {
	b, err := v.Read(name, guid)
	if err != nil {
		return "", err
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00"), nil
}

// UTF16 is s as UEFI stores a string: UTF-16, little-endian, NUL-terminated.
func UTF16(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}
