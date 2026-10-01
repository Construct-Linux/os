package update

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"

	"github.com/spin-stack/spindrift/internal/gpt"
)

// LoaderPath is where an installed ESP holds the systemd-boot the boot entry starts, as firmware
// writes a path: the one systemd's tools install to. The removable path, EFI/BOOT/BOOTX64.EFI,
// holds the same bytes for firmware that boots without an entry, or loses it.
const LoaderPath = `\EFI\systemd\systemd-bootx64.efi`

// EntryTitle is what the firmware's boot menu calls the entry.
const EntryTitle = "Spindrift"

// BootEntry makes the firmware whose variables are vars start systemd-boot from the ESP esp of the
// disk whose table is disk: a Boot#### entry, first in BootOrder. An entry of the same title and
// ESP - a machine installed again - is replaced rather than added to. It answers the entry's
// number. spin-ami's installer's, which wrote what efibootmgr writes without needing it.
func BootEntry(vars Vars, disk gpt.Table, esp gpt.Partition) (uint16, error) {
	i := slices.Index(disk.Partitions, esp)
	if i < 0 || esp.Type != gpt.TypeESP {
		return 0, fmt.Errorf("install: %q is not an ESP of the disk", esp.Name)
	}
	// Partitions are numbered from one, in the order of their entries.
	option, err := loadOption(uint32(i+1), esp)
	if err != nil {
		return 0, err
	}
	n, err := entryNumber(vars, option)
	if err != nil {
		return 0, err
	}
	if err := vars.Write(fmt.Sprintf("Boot%04X", n), Global, NVBootRuntime, option); err != nil {
		return 0, err
	}
	order, err := vars.Read("BootOrder", Global)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err // mutate-exempt: the number beside an error is nobody's to read
	}
	next := binary.LittleEndian.AppendUint16(nil, n)
	for i := 0; i+1 < len(order); i += 2 {
		if e := binary.LittleEndian.Uint16(order[i:]); e != n {
			next = binary.LittleEndian.AppendUint16(next, e)
		}
	}
	return n, vars.Write("BootOrder", Global, NVBootRuntime, next)
}

// bootVariable is a Boot#### variable's name.
var bootVariable = regexp.MustCompile(`^Boot([0-9A-F]{4})-` + Global + `$`)

// entryNumber is the number of the entry holding option already, or the lowest no entry has.
func entryNumber(vars Vars, option []byte) (uint16, error) {
	entries, err := os.ReadDir(string(vars))
	if err != nil {
		return 0, err
	}
	var taken []uint16
	for _, e := range entries {
		m := bootVariable.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		// Four hex digits, by the pattern.
		n, _ := strconv.ParseUint(m[1], 16, 16) // mutate-exempt: four hex digits fit in any bit size past 16
		body, err := vars.Read("Boot"+m[1], Global)
		if err != nil {
			return 0, err
		}
		if bytes.Equal(body, option) {
			return uint16(n), nil
		}
		taken = append(taken, uint16(n))
	}
	for n := range uint16(0xffff) {
		if !slices.Contains(taken, n) {
			return n, nil
		}
	}
	return 0, errors.New("install: the firmware has no free boot entry")
}

// loadOption is an active EFI_LOAD_OPTION titled EntryTitle for LoaderPath on esp, partition
// number of its disk: a hard drive device path - the partition by its number, its sectors and its
// GUID, as efibootmgr writes one and firmware finds a partition by - then the file.
func loadOption(number uint32, esp gpt.Partition) ([]byte, error) {
	// The partition's GUID as its disk's table stores it, which is what the firmware compares.
	guid, err := gpt.GUID(esp.UUID)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	var path []byte
	// Hard drive: type 4 (media), subtype 1, 42 bytes.
	path = append(path, 4, 1)
	path = le.AppendUint16(path, 42)
	path = le.AppendUint32(path, number)
	path = le.AppendUint64(path, uint64(esp.First))
	path = le.AppendUint64(path, uint64(esp.Last-esp.First+1))
	path = append(path, guid...)
	path = append(path, 2, 2) // a GPT partition, known by its GUID
	// File: type 4, subtype 4.
	file := UTF16(LoaderPath)
	path = append(path, 4, 4)
	path = le.AppendUint16(path, uint16(4+len(file)))
	path = append(path, file...)
	// The end of the path.
	path = append(path, 0x7f, 0xff, 4, 0)

	var o []byte
	o = le.AppendUint32(o, 1) // LOAD_OPTION_ACTIVE
	o = le.AppendUint16(o, uint16(len(path)))
	o = append(o, UTF16(EntryTitle)...)
	return append(o, path...), nil
}
