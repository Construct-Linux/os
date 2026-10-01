package update

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ulikunitz/xz"
)

// nopClose is the disk, which the test closes, as Install's own.
type nopClose struct{ *os.File }

func (nopClose) Close() error { return nil }

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

// rootSize is a test release's root, decompressed, and a little more.
const rootSize = 4 << 20

// release is a release of version for a slot of the UUIDs root and verity, in a directory of its
// own, signed by signer: a root and a hash tree of their byte, xz-compressed as published, a kernel image that names them, and
// a file that is none of a machine's business. It answers the directory and the root's bytes.
func release(t *testing.T, version, root, verity string, signer *openpgp.Entity, fill byte) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	rootFile, verityFile, uki := Names(version, root, verity)
	body := bytes.Repeat([]byte{fill}, rootSize-4096)
	write(t, filepath.Join(dir, rootFile), string(xzOf(t, body)))
	write(t, filepath.Join(dir, verityFile), string(xzOf(t, bytes.Repeat([]byte{fill + 1}, 64<<10))))
	cmdline := `dm-mod.create="root,,,ro,0 8 verity 1 PARTUUID=` + root + ` PARTUUID=` + verity + ` 4096 4096 1 0 sha256 00 00" dm-mod.waitfor=PARTUUID=` +
		root + `,PARTUUID=` + verity + ` root=/dev/dm-0`
	write(t, filepath.Join(dir, uki), string(peImage(t, map[string]string{".osrel": "IMAGE_ID=" + ImageID + "\nIMAGE_VERSION=" + version + "\n", ".cmdline": cmdline})))
	write(t, filepath.Join(dir, "README"), "not a machine's")
	seal(t, dir, signer)
	return dir, body
}

// xzOf is b compressed as the build compresses a release's partitions.
func xzOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	must(t, err)
	_, err = w.Write(b)
	must(t, err)
	must(t, w.Close())
	return out.Bytes()
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
