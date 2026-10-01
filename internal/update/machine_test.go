package update

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/spin-desktop/internal/gpt"
)

// What testMachine's disk holds: an ESP, two slots of a small root and a smaller hash tree - slot
// A version 1.0, slot B empty - and /var, in 512-byte sectors. Versions are short here; Compare
// orders the desktop's UTC times as it orders these (version_test.go).
const (
	diskSize   = 64 << 20
	rootA      = "a201682a-bb1c-9d0b-7b31-5d957af007b9"
	verityA    = "3f3c0a48-7b27-5b3b-73c2-385422d45830"
	emptyRoot  = "11111111-2222-3333-4444-555555555555"
	emptyHash  = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	rootSize   = 4 << 20
	verityHash = 1 << 20
)

// testLayout is the table testMachine's disk starts with.
func testLayout() gpt.Table {
	at := func(mib int64) int64 { return mib << 20 / gpt.Sector }
	return gpt.Table{Disk: "0b6d8f0e-1b1a-4f55-9d6c-9f1e5c7b3a21", Partitions: []gpt.Partition{
		{Type: gpt.TypeESP, UUID: "8db37c3e-fa8d-e299-a792-c04533fff123", First: at(1), Last: at(9) - 1, Name: "esp"},
		{Type: TypeRoot, UUID: rootA, First: at(9), Last: at(13) - 1, Name: ImageID + "_1.0"},
		{Type: TypeRootVerity, UUID: verityA, First: at(13), Last: at(14) - 1, Name: ImageID + "_1.0"},
		{Type: TypeRoot, UUID: emptyRoot, First: at(14), Last: at(18) - 1, Name: Empty},
		{Type: TypeRootVerity, UUID: emptyHash, First: at(18), Last: at(19) - 1, Name: Empty},
		{Type: gpt.TypeLinuxData, UUID: "7b351e62-42e9-a0a8-d4f5-ac74b3c38dd9", First: at(19), Last: gpt.LastUsable(diskSize), Name: "var"},
	}}
}

// machine is a desktop as an update sees it: its disk, the sysfs that says which slot the root is
// on, the ESP, its os-release, and the systemd it asks.
type machine struct {
	dir, esp, osRelease, sys string
	disk                     *os.File
	system                   *fakeSystem
	log                      strings.Builder
}

// testMachine is a machine running 1.0 from slot A, blessed, with slot B empty.
func testMachine(t *testing.T) *machine {
	t.Helper()
	m := &machine{dir: t.TempDir(), system: &fakeSystem{active: map[string]bool{}}}
	m.esp, m.osRelease, m.sys = filepath.Join(m.dir, "efi"), filepath.Join(m.dir, "os-release"), filepath.Join(m.dir, "sys")
	f, err := os.Create(filepath.Join(m.dir, "disk"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(diskSize); err != nil {
		t.Fatal(err)
	}
	if err := gpt.Write(f, diskSize, testLayout()); err != nil {
		t.Fatal(err)
	}
	// What slot A holds, so a write that lands there shows.
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xa}, rootSize), 9<<20); err != nil {
		t.Fatal(err)
	}
	m.disk = f
	m.boot(t, "1.0", 9<<20/gpt.Sector, 13<<20/gpt.Sector)
	write(t, filepath.Join(m.esp, Linux, ImageID+"_1.0.efi"), "booted")
	return m
}

// boot makes the machine one running version from the slot whose partitions start at root and
// verity: its os-release and the sysfs under its root's device.
func (m *machine) boot(t *testing.T, version string, root, verity int64) {
	t.Helper()
	write(t, m.osRelease, "NAME=\"Spin Desktop\"\nIMAGE_ID="+ImageID+"\nIMAGE_VERSION="+version+"\n")
	_ = os.RemoveAll(m.sys)
	disk := filepath.Join(m.sys, "devices", "nvme0n1")
	write(t, filepath.Join(disk, "queue", "logical_block_size"), "512\n")
	for i, start := range []int64{root, verity} {
		name := fmt.Sprintf("nvme0n1p%d", i+2)
		write(t, filepath.Join(disk, name, "partition"), fmt.Sprint(i+2))
		write(t, filepath.Join(disk, name, "start"), fmt.Sprint(start))
		link(t, filepath.Join("..", "..", "devices", "nvme0n1", name), filepath.Join(m.sys, "class", "block", name))
		write(t, filepath.Join(m.sys, "dev", "block", "253:0", "slaves", name), "")
	}
	link(t, filepath.Join("..", "..", "devices", "nvme0n1"), filepath.Join(m.sys, "class", "block", "nvme0n1"))
}

// options are Run's on m, reading the release under source.
func (m *machine) options(t *testing.T, source string) Options {
	return Options{
		Keys: trusted(t), OSRelease: m.osRelease, Source: source, Sys: m.sys, Root: unix.Mkdev(253, 0),
		OpenDisk: func(path string) (Disk, error) {
			if path != "/dev/nvme0n1" {
				return nil, fmt.Errorf("opened %s, not the root's disk", path)
			}
			return nopClose{m.disk}, nil
		},
		ESP: m.esp, Log: slog.New(slog.NewTextHandler(&m.log, nil)),
	}
}

// nopClose is the disk, which the test closes, as Run's own.
type nopClose struct{ *os.File }

func (nopClose) Close() error { return nil }

// table is the disk's table as it is now.
func (m *machine) table(t *testing.T) gpt.Table {
	t.Helper()
	tb, err := gpt.Read(m.disk)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

// images are the names of the kernel images on the ESP, sorted.
func (m *machine) images(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(m.esp, Linux))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// at is size bytes of the disk from off.
func (m *machine) at(t *testing.T, off, size int64) []byte {
	t.Helper()
	b := make([]byte, size)
	if _, err := m.disk.ReadAt(b, off); err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeSystem is systemd as a boot check sees it: which units are active, and the reboots asked
// for.
type fakeSystem struct {
	mu      sync.Mutex
	active  map[string]bool
	reboots int
}

func (s *fakeSystem) Active(_ context.Context, unit string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[unit], nil
}

func (s *fakeSystem) Reboot(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reboots++
	return nil
}

// release is a release of version for a slot of the UUIDs root and verity, in a directory of its
// own, signed by signer: a root and a hash tree of their byte, a kernel image that names them, and
// a file that is none of a machine's business. It answers the directory and the root's bytes.
func release(t *testing.T, version, root, verity string, signer *openpgp.Entity, fill byte) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	rootFile, verityFile, uki := Names(version, root, verity)
	body := bytes.Repeat([]byte{fill}, rootSize-4096)
	write(t, filepath.Join(dir, rootFile), string(body))
	write(t, filepath.Join(dir, verityFile), strings.Repeat(string([]byte{fill + 1}), 64<<10))
	cmdline := `dm-mod.create="root,,,ro,0 8 verity 1 PARTUUID=` + root + ` PARTUUID=` + verity + ` 4096 4096 1 0 sha256 00 00" dm-mod.waitfor=PARTUUID=` +
		root + `,PARTUUID=` + verity + ` root=/dev/dm-0`
	write(t, filepath.Join(dir, uki), string(peImage(t, map[string]string{".osrel": "IMAGE_ID=" + ImageID + "\nIMAGE_VERSION=" + version + "\n", ".cmdline": cmdline})))
	write(t, filepath.Join(dir, "README"), "not a machine's")
	seal(t, dir, signer)
	return dir, body
}

// seal lists dir and signs the list, as the build does with gpg.
func seal(t *testing.T, dir string, signer *openpgp.Entity) {
	t.Helper()
	must(t, WriteSums(dir))
	sums, err := os.ReadFile(filepath.Join(dir, SumsFile))
	must(t, err)
	write(t, filepath.Join(dir, SignatureFile), string(sign(t, signer, sums)))
}

// peImage is a PE image of nothing but sections, which is all a kernel image is read for.
func peImage(t *testing.T, sections map[string]string) []byte {
	t.Helper()
	le := binary.LittleEndian
	names := slices.Sorted(maps.Keys(sections))
	var b bytes.Buffer
	dos := make([]byte, 64)
	copy(dos, "MZ")
	le.PutUint32(dos[0x3c:], 64)
	b.Write(dos)
	b.WriteString("PE\x00\x00")
	opt := pe.OptionalHeader64{Magic: 0x20b, FileAlignment: 512, SectionAlignment: 4096, NumberOfRvaAndSizes: 16}
	must(t, binary.Write(&b, le, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, NumberOfSections: uint16(len(names)),
		SizeOfOptionalHeader: uint16(binary.Size(opt)), Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE}))
	must(t, binary.Write(&b, le, opt))
	offset := (b.Len() + 40*len(names) + 511) / 512 * 512
	var data bytes.Buffer
	for i, name := range names {
		raw := (len(sections[name]) + 511) / 512 * 512
		h := pe.SectionHeader32{VirtualSize: uint32(len(sections[name])), VirtualAddress: uint32(0x1000 * (i + 1)),
			SizeOfRawData: uint32(raw), PointerToRawData: uint32(offset + data.Len())}
		copy(h.Name[:], name)
		must(t, binary.Write(&b, le, h))
		data.WriteString(sections[name])
		data.Write(make([]byte, raw-len(sections[name])))
	}
	b.Write(make([]byte, offset-b.Len()))
	b.Write(data.Bytes())
	return b.Bytes()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(body), 0o644))
}

func link(t *testing.T, target, p string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.Symlink(target, p))
}

// otherKey is a key no machine trusts.
func otherKey() *openpgp.Entity { return testKeys()[1] }

// signer is the release's key.
func signer() *openpgp.Entity { return testKeys()[0] }
