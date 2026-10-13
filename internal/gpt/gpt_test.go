package gpt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Partition types the tests lay besides the ESP, as UEFI has them.
const (
	TypeLinuxData = "0fc63daf-8483-4772-8e79-3d69d8477de4"
	TypeBasicData = "ebd0a0a2-b9e5-4433-87c0-68b6b72699c7"
)

func disk(t *testing.T, size int64) *os.File {
	f, err := os.Create(filepath.Join(t.TempDir(), "disk"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return f
}

// What is written is what is read back: every partition's types, sectors and name, and the disk's
// GUID, with the checksums a firmware and the kernel check.
func TestATableIsReadAsItWasWritten(t *testing.T) {
	f := disk(t, 64<<20)
	want := Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21", Partitions: []Partition{
		{Type: TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: 2048, Last: 4095, Name: "construct-live", Number: 1},
		{Type: TypeESP, UUID: "8db37c3e-fa8d-e299-a792-c04533fff123", First: 4096, Last: 8191, Name: "esp ü", Attrs: 1 << 60, Number: 2},
		{Type: TypeBasicData, UUID: "00000000-0000-0000-0000-000000000001", First: 8192, Last: LastUsable(64 << 20), Name: strings.Repeat("n", 36), Number: 3},
	}}
	if err := Write(f, 64<<20, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(f)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read %+v, %v\nwant %+v", got, err, want)
	}
	if p, err := got.Named("esp ü"); err != nil || p.Offset() != 4096*512 || p.Size() != 4096*512 {
		t.Errorf("Named = %+v, %v", p, err)
	}
	if _, err := got.Named("var"); err == nil {
		t.Error("a partition nobody wrote was found")
	}
	// The backup header points back at the primary, from the disk's last sector.
	backup := make([]byte, 92)
	if _, err := f.ReadAt(backup, 64<<20-512); err != nil || string(backup[:8]) != "EFI PART" {
		t.Errorf("no backup header at the end: %q, %v", backup[:8], err)
	}
	mbr := make([]byte, 512)
	if _, err := f.ReadAt(mbr, 0); err != nil || mbr[450] != 0xee || mbr[510] != 0x55 || mbr[511] != 0xaa {
		t.Errorf("no protective MBR: %v", err)
	}
}

// tableHeader is the GPT header at sector lba of f, by the fields a firmware and the kernel check it by.
type tableHeader struct {
	revision                         uint32
	my, alternate, first, last, from int64
	crcOK                            bool
}

// readHeader is the header at sector lba of f, and the entries it names.
func readHeader(t *testing.T, f *os.File, lba int64) (tableHeader, []byte) {
	t.Helper()
	h := make([]byte, 92)
	if _, err := f.ReadAt(h, lba*512); err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	got := tableHeader{revision: le.Uint32(h[8:]), my: int64(le.Uint64(h[24:])), alternate: int64(le.Uint64(h[32:])),
		first: int64(le.Uint64(h[40:])), last: int64(le.Uint64(h[48:])), from: int64(le.Uint64(h[72:]))}
	sum := le.Uint32(h[16:])
	le.PutUint32(h[16:], 0)
	entries := make([]byte, 128*128)
	if _, err := f.ReadAt(entries, got.from*512); err != nil {
		t.Fatal(err)
	}
	got.crcOK = crc32.ChecksumIEEE(h) == sum && crc32.ChecksumIEEE(entries) == le.Uint32(h[88:])
	return got, entries
}

// Both copies of the table are where each other says, over the same usable sectors, with the same
// entries and their checksums right: a firmware that finds the primary damaged boots from the
// backup. The protective MBR claims the whole disk as the specification's one partition.
func TestBothTablesAndTheMBRAreWhereFirmwareLooks(t *testing.T) {
	const size = 64 << 20
	f := disk(t, size)
	if err := Write(f, size, Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21", Partitions: []Partition{
		{Type: TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: 2048, Last: 4095, Name: "a"},
	}}); err != nil {
		t.Fatal(err)
	}
	last := int64(size/512 - 1)
	primary, primaryEntries := readHeader(t, f, 1)
	backup, backupEntries := readHeader(t, f, last)
	if (primary != tableHeader{revision: 0x10000, my: 1, alternate: last, first: 34, last: last - 33, from: 2, crcOK: true}) {
		t.Errorf("the primary header is %+v", primary)
	}
	if (backup != tableHeader{revision: 0x10000, my: last, alternate: 1, first: 34, last: last - 33, from: last - 32, crcOK: true}) {
		t.Errorf("the backup header is %+v", backup)
	}
	if !bytes.Equal(primaryEntries, backupEntries) || LastUsable(size) != last-33 || firstUsable != 34 {
		t.Error("the two tables differ, or the usable sectors are not the headers'")
	}
	mbr := make([]byte, 16)
	if _, err := f.ReadAt(mbr, 446); err != nil {
		t.Fatal(err)
	}
	// Status, CHS of sector 1, type 0xEE, the largest CHS, from LBA 1 for the rest of the disk.
	want := []byte{0, 0, 2, 0, 0xee, 0xff, 0xff, 0xff, 1, 0, 0, 0}
	want = binary.LittleEndian.AppendUint32(want, uint32(size/512-1))
	if !bytes.Equal(mbr, want) {
		t.Errorf("the protective MBR's partition is % x, want % x", mbr, want)
	}
	// A disk past 2 TiB is claimed as far as 32 bits reach.
	if got := binary.LittleEndian.Uint32(protectiveMBR(1 << 42)[458:]); got != 0xffffffff {
		t.Errorf("a 4 TiB disk's MBR claims %d sectors", got)
	}
}

// The smallest disk a table fits is two tables' worth of sectors; one smaller, or one that is not
// whole sectors, has none.
func TestATableNeedsRoomForBothCopies(t *testing.T) {
	const smallest = 2 * firstUsable * 512
	if err := Write(disk(t, smallest), smallest, Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21"}); err != nil {
		t.Errorf("the smallest disk took no table: %v", err)
	}
	for _, size := range []int64{smallest - 512, smallest + 1} {
		if err := Write(disk(t, size+512), size, Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21"}); err == nil {
			t.Errorf("a disk of %d bytes took a table", size)
		}
	}
}

// A table whose header says what no table's does - entries too small, too large, not a power of
// two, too many - or whose entry runs backwards or past any disk, is refused though its checksums
// are right.
func TestATableItsOwnHeaderContradictsIsNotRead(t *testing.T) {
	le := binary.LittleEndian
	entries := func(count, size uint32) func(h, _ []byte) {
		return func(h, _ []byte) { le.PutUint32(h[80:], count); le.PutUint32(h[84:], size) }
	}
	for name, c := range map[string]struct {
		change func(h, e []byte)
		fails  string
	}{
		"entries of 64 bytes":     {entries(256, 64), "not a table"},
		"entries of 8192 bytes":   {entries(2, 8192), "not a table"},
		"entries of 384 bytes":    {entries(64, 384), "not a table"},
		"16385 entries":           {entries(16385, 128), "not a table"},
		"an entry ending first":   {func(_, e []byte) { le.PutUint64(e[40:], 100) }, "runs from sector"},
		"an entry past any disk":  {func(_, e []byte) { le.PutUint64(e[40:], 1<<50+1) }, "runs from sector"},
		"another header's length": {func(h, _ []byte) { le.PutUint32(h[12:], 96) }, "checksum"},
		// What a table may say, and is read as it says.
		"entries of 4096 bytes": {entries(4, 4096), ""},
		"16384 entries":         {entries(16384, 128), ""},
	} {
		t.Run(name, func(t *testing.T) {
			tb, err := Read(patched(t, c.change))
			switch {
			case c.fails == "" && (err != nil || len(tb.Partitions) != 1):
				t.Errorf("read %+v, %v", tb, err)
			case c.fails != "" && (err == nil || !strings.Contains(err.Error(), c.fails)):
				t.Errorf("read %+v, %v; want it refused as %q", tb, err, c.fails)
			}
		})
	}
}

// patched is a disk of one partition whose primary header and entries change changes, with their
// checksums made right again over as many entries as the header then says.
func patched(t *testing.T, change func(h, e []byte)) *os.File {
	t.Helper()
	const size = 64 << 20
	le := binary.LittleEndian
	f := disk(t, size)
	if err := Write(f, size, Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21", Partitions: []Partition{
		{Type: TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: 2048, Last: 4095, Name: "a"},
	}}); err != nil {
		t.Fatal(err)
	}
	h := make([]byte, 92)
	if _, err := f.ReadAt(h, 512); err != nil {
		t.Fatal(err)
	}
	change(h, make([]byte, 128))
	e := make([]byte, int(le.Uint32(h[80:]))*int(le.Uint32(h[84:])))
	if _, err := f.ReadAt(e, 1024); err != nil {
		t.Fatal(err)
	}
	change(h, e)
	le.PutUint32(h[88:], crc32.ChecksumIEEE(e))
	le.PutUint32(h[16:], 0)
	le.PutUint32(h[16:], crc32.ChecksumIEEE(h))
	if _, err := f.WriteAt(h, 512); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(e, 1024); err != nil {
		t.Fatal(err)
	}
	return f
}

// A table that would not be one - a partition over the tables or past the disk, two that overlap,
// a name too long, a GUID that is not one - is never written.
func TestATableThatCannotBeIsRefused(t *testing.T) {
	const size = 64 << 20
	ok := Partition{Type: TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: 2048, Last: 4095, Name: "a"}
	for name, parts := range map[string][]Partition{
		"over the primary table":  {{Type: ok.Type, UUID: ok.UUID, First: firstUsable - 1, Last: 100}},
		"over the backup table":   {{Type: ok.Type, UUID: ok.UUID, First: 2048, Last: LastUsable(size) + 1}},
		"ending before it starts": {{Type: ok.Type, UUID: ok.UUID, First: 4096, Last: 2048}},
		"overlapping":             {ok, {Type: ok.Type, UUID: ok.UUID, First: 4095, Last: 5000, Name: "b"}},
		"a long name":             {{Type: ok.Type, UUID: ok.UUID, First: 2048, Last: 4095, Name: strings.Repeat("n", 37)}},
		"a bad GUID":              {{Type: "esp", UUID: ok.UUID, First: 2048, Last: 4095}},
	} {
		if err := Write(disk(t, size), size, Table{Disk: ok.UUID, Partitions: parts}); err == nil {
			t.Errorf("a table with a partition %s was written", name)
		}
	}
	if err := Write(disk(t, 4096), 4096, Table{Disk: ok.UUID}); err == nil {
		t.Error("a table was written on a disk too small for one")
	}
}

// A table whose bytes changed after it was written is not taken for the medium's.
func TestAChangedTableIsNotRead(t *testing.T) {
	const size = 64 << 20
	for _, at := range []int64{512 + 40, 2*512 + 70} {
		f := disk(t, size)
		if err := Write(f, size, Table{Disk: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", Partitions: []Partition{
			{Type: TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: 2048, Last: 4095, Name: "a"},
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte{0xff}, at); err != nil {
			t.Fatal(err)
		}
		if tb, err := Read(f); err == nil {
			t.Errorf("a table changed at byte %d was read: %+v", at, tb)
		}
	}
	if _, err := Read(disk(t, size)); err == nil {
		t.Error("an empty disk has a table")
	}
}
