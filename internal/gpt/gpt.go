// Package gpt reads and writes a GUID partition table on 512-byte sectors: the table the installer
// lays on a machine's disk (internal/update), and the stick's that cmd/image forges and reads.
package gpt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/bits"
	"strings"
	"unicode/utf16"
)

// Sector is the table's sector: the disks it is read and written on have 512-byte logical
// sectors, which the installer checks before it touches one.
const Sector = 512

const (
	entries   = 128 // the entries a table has room for, as every tool writes one
	entrySize = 128
	// tableSectors is the sectors the entries take: 128 entries of 128 bytes.
	tableSectors = entries * entrySize / Sector
	headerSize   = 92
	// nameUnits is how many UTF-16 code units a partition's name has room for.
	nameUnits = 36
)

// Partition types this repository names, as the Discoverable Partitions Specification and UEFI
// have them.
const (
	TypeESP = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
)

// Partition is one entry: its type and unique GUIDs, lowercase, its first and last sector, its
// name, which systemd's tools call its label, and its attribute bits - systemd-repart's ReadOnly=
// is bit 60 - which a table written again keeps.
type Partition struct {
	Type, UUID  string
	First, Last int64
	Name        string
	Attrs       uint64
	// Number is the entry's place in the table, from 1: the kernel's partition number, which a
	// table read gives and a table written takes from the order of Partitions.
	Number int
}

// Offset and Size are the partition's, in bytes.
func (p Partition) Offset() int64 { return p.First * Sector }
func (p Partition) Size() int64   { return (p.Last - p.First + 1) * Sector }

// Table is a disk's GUID and its partitions, in the order of their entries.
type Table struct {
	Disk       string
	Partitions []Partition
}

// Named is the partition called name.
func (t Table) Named(name string) (Partition, error) {
	for _, p := range t.Partitions {
		if p.Name == name {
			return p, nil
		}
	}
	return Partition{}, fmt.Errorf("gpt: no partition %q", name)
}

// FirstUsable is the first sector a partition may start at, after the primary table.
const FirstUsable = 2 + tableSectors

// LastUsable is the last sector a partition may end at on a disk of size bytes, before the backup
// table.
func LastUsable(size int64) int64 { return size/Sector - 2 - tableSectors }

// Write puts t on w, a disk of size bytes: a protective MBR, the primary table after it and the
// backup at the end. A partition outside the usable sectors, or two that overlap, is refused.
func Write(w io.WriterAt, size int64, t Table) error {
	if size%Sector != 0 || size/Sector < 2*(FirstUsable) {
		return fmt.Errorf("gpt: a disk of %d bytes has no room for a table", size)
	}
	if len(t.Partitions) > entries {
		return fmt.Errorf("gpt: %d partitions, and a table has room for %d", len(t.Partitions), entries)
	}
	table := make([]byte, entries*entrySize)
	for i, p := range t.Partitions {
		if err := p.check(size, t.Partitions[:i]); err != nil {
			return err
		}
		e := table[i*entrySize:]
		if err := putGUID(e[0:], p.Type); err != nil {
			return err
		}
		if err := putGUID(e[16:], p.UUID); err != nil {
			return err
		}
		binary.LittleEndian.PutUint64(e[32:], uint64(p.First))
		binary.LittleEndian.PutUint64(e[40:], uint64(p.Last))
		binary.LittleEndian.PutUint64(e[48:], p.Attrs)
		for j, u := range utf16.Encode([]rune(p.Name)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], u)
		}
	}
	last := size/Sector - 1
	primary, err := header(t.Disk, 1, last, 2, size, table)
	if err != nil {
		return err
	}
	backup, err := header(t.Disk, last, 1, last-tableSectors, size, table)
	if err != nil {
		return err
	}
	for _, part := range []struct {
		at   int64
		body []byte
	}{
		{0, protectiveMBR(size)},
		{Sector, primary},
		{2 * Sector, table},
		{(last - tableSectors) * Sector, table},
		{last * Sector, backup},
	} {
		if _, err := w.WriteAt(part.body, part.at); err != nil {
			return err
		}
	}
	return nil
}

// check refuses p on a disk of size bytes if it is not a partition a table can hold, or it
// overlaps one of before.
func (p Partition) check(size int64, before []Partition) error {
	switch {
	case p.First < FirstUsable || p.Last > LastUsable(size) || p.First > p.Last:
		return fmt.Errorf("gpt: %q runs from sector %d to %d, outside %d to %d", p.Name, p.First, p.Last, FirstUsable, LastUsable(size))
	case len(utf16.Encode([]rune(p.Name))) > nameUnits:
		return fmt.Errorf("gpt: %q is longer than a partition's name may be", p.Name)
	}
	for _, q := range before {
		if p.First <= q.Last && q.First <= p.Last {
			return fmt.Errorf("gpt: %q overlaps %q", p.Name, q.Name)
		}
	}
	return nil
}

// header is a table's header at sector at, whose other copy is at alternate and whose entries are
// table, from sector entriesAt.
func header(disk string, at, alternate, entriesAt, size int64, table []byte) ([]byte, error) {
	h := make([]byte, Sector)
	copy(h, "EFI PART")
	le := binary.LittleEndian
	le.PutUint32(h[8:], 0x00010000)
	le.PutUint32(h[12:], headerSize)
	le.PutUint64(h[24:], uint64(at))
	le.PutUint64(h[32:], uint64(alternate))
	le.PutUint64(h[40:], FirstUsable)
	le.PutUint64(h[48:], uint64(LastUsable(size)))
	if err := putGUID(h[56:], disk); err != nil {
		return nil, err
	}
	le.PutUint64(h[72:], uint64(entriesAt))
	le.PutUint32(h[80:], entries)
	le.PutUint32(h[84:], entrySize)
	le.PutUint32(h[88:], crc32.ChecksumIEEE(table))
	le.PutUint32(h[16:], crc32.ChecksumIEEE(h[:headerSize]))
	return h, nil
}

// protectiveMBR is the MBR that tells a tool which reads only MBRs that the disk is taken: one
// partition of type 0xEE over all of it, up to what 32 bits of sectors say.
func protectiveMBR(size int64) []byte {
	m := make([]byte, Sector)
	e := m[446:]
	// Not bootable, CHS of sector 1, type 0xEE, the largest CHS there is.
	copy(e, []byte{0x00, 0x00, 0x02, 0x00, 0xee, 0xff, 0xff, 0xff})
	binary.LittleEndian.PutUint32(e[8:], 1)
	binary.LittleEndian.PutUint32(e[12:], uint32(min(size/Sector-1, 0xffffffff)))
	m[510], m[511] = 0x55, 0xaa
	return m
}

// Read is the primary table of r. Its checksums are checked: the installer copies what it finds by
// it onto a machine's disk.
func Read(r io.ReaderAt) (Table, error) {
	h, table, err := readTable(r, 1)
	if err != nil {
		return Table{}, err
	}
	le := binary.LittleEndian
	count, size := le.Uint32(h[80:]), le.Uint32(h[84:])
	t := Table{Disk: guid(h[56:])}
	for i := range int(count) {
		e := table[i*int(size):]
		// An entry with no type is an unused one, whatever else it says.
		if bytes.Equal(e[:16], make([]byte, 16)) {
			continue
		}
		units := make([]uint16, nameUnits)
		for j := range units {
			units[j] = le.Uint16(e[56+2*j:])
		}
		p := Partition{
			Type: guid(e), UUID: guid(e[16:]),
			First: int64(le.Uint64(e[32:])), Last: int64(le.Uint64(e[40:])),
			Name:   strings.TrimRight(string(utf16.Decode(units)), "\x00"),
			Attrs:  le.Uint64(e[48:]),
			Number: i + 1,
		}
		if p.First > p.Last || p.Last > 1<<50 {
			return Table{}, fmt.Errorf("gpt: %q runs from sector %d to %d", p.Name, p.First, p.Last)
		}
		t.Partitions = append(t.Partitions, p)
	}
	return t, nil
}

// readTable is the header at sector lba of r, the whole sector, and the entries it names, both
// checked against their checksums.
func readTable(r io.ReaderAt, lba int64) (header, table []byte, err error) {
	h := make([]byte, Sector)
	if _, err := r.ReadAt(h, lba*Sector); err != nil {
		return nil, nil, fmt.Errorf("gpt: reading the header: %w", err)
	}
	if string(h[:8]) != "EFI PART" {
		return nil, nil, errors.New("gpt: no GUID partition table")
	}
	le := binary.LittleEndian
	check := append([]byte(nil), h[:headerSize]...)
	le.PutUint32(check[16:], 0)
	if le.Uint32(h[12:]) != headerSize || crc32.ChecksumIEEE(check) != le.Uint32(h[16:]) {
		return nil, nil, errors.New("gpt: the header's checksum is wrong")
	}
	count, size := le.Uint32(h[80:]), le.Uint32(h[84:])
	// The header is the disk's word, and a slice is cut by it: an entry is 128 bytes times a power
	// of two (UEFI 5.3.2), and no table a tool writes has more than a few thousand.
	notATable := fmt.Errorf("gpt: %d entries of %d bytes is not a table", count, size)
	switch {
	case size < entrySize, bits.OnesCount32(size) != 1:
		return nil, nil, notATable
	case size > 4096: // mutate-exempt: no power of two is past 4096 and not past 4097
		return nil, nil, notATable
	case count > 16384:
		return nil, nil, notATable
	}
	table = make([]byte, int(count)*int(size))
	if _, err := r.ReadAt(table, int64(le.Uint64(h[72:]))*Sector); err != nil {
		return nil, nil, fmt.Errorf("gpt: reading the entries: %w", err)
	}
	if crc32.ChecksumIEEE(table) != le.Uint32(h[88:]) {
		return nil, nil, errors.New("gpt: the entries' checksum is wrong")
	}
	return h, table, nil
}

// guid is b as a GUID is written: its first three fields are stored little-endian.
func guid(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", binary.LittleEndian.Uint32(b[0:]), binary.LittleEndian.Uint16(b[4:]),
		binary.LittleEndian.Uint16(b[6:]), b[8:10], b[10:16])
}

// GUID is s as UEFI stores a GUID - its first three fields little-endian - as a table holds it
// and a boot entry's device path names a partition.
func GUID(s string) ([]byte, error) {
	b := make([]byte, 16)
	return b, putGUID(b, s)
}

// putGUID stores s at b as guid reads it.
func putGUID(b []byte, s string) error {
	var raw [16]byte // mutate-exempt: a longer buffer's extra zero lands where the next field is written after it
	var d1 uint32
	var d2, d3 uint16
	if len(s) != 36 {
		return fmt.Errorf("gpt: %q is not a GUID", s)
	}
	if _, err := fmt.Sscanf(s, "%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x", &d1, &d2, &d3,
		&raw[8], &raw[9], &raw[10], &raw[11], &raw[12], &raw[13], &raw[14], &raw[15]); err != nil {
		return fmt.Errorf("gpt: %q is not a GUID: %w", s, err)
	}
	binary.LittleEndian.PutUint32(raw[0:], d1)
	binary.LittleEndian.PutUint16(raw[4:], d2)
	binary.LittleEndian.PutUint16(raw[6:], d3)
	copy(b, raw[:])
	return nil
}
