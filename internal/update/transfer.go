package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spin-stack/spin-desktop/internal/gpt"
)

// Disk is the disk the slots are on, open to be read and written: the running root's.
type Disk interface {
	gpt.Disk
	Sync() error
}

// Fetch opens the file of the release called name, and says how long it is when it knows (-1
// when it does not).
type Fetch func(ctx context.Context, name string) (io.ReadCloser, int64, error)

// maxImage is the most a kernel image may be: the ESP holds two, and is 512 MiB.
const maxImage = 200 << 20

// Write writes r into target, a slot of disk that is not the booted one, and r's kernel image into
// the ESP at esp beside the booted one's, for systemd-boot to try Tries times - what
// systemd-sysupdate's three transfers of a slot's root, its hash tree and its kernel image do, in
// its order, so that a write cut off anywhere leaves a machine that boots what it booted before:
//
//  1. every kernel image but the booted version's is removed, the target's old version's among
//     them: nothing boots the slot while it is written, and the ESP keeps two versions at most;
//  2. the target's partitions are labelled Empty;
//  3. the root and the hash tree are written into them, each checked against its digest in the
//     release as it is read - one that is not is a slot left Empty;
//  4. the partitions are given the version's label and the UUIDs the root hash gives them, which
//     the kernel image's command line names;
//  5. the kernel image is written beside the booted one's under a name systemd-boot does not read,
//     checked against its digest and held to naming the slot it boots, and renamed to
//     <ImageID>_<version>+<Tries>.efi: only then does systemd-boot see it, and boot it first.
func Write(ctx context.Context, disk Disk, esp string, target Slot, booted string, r Release, fetch Fetch) error {
	if err := removeImages(esp, booted); err != nil {
		return err
	}
	for _, p := range []gpt.Partition{target.Root, target.Verity} {
		if err := gpt.Rename(disk, p.UUID, p.UUID, Empty); err != nil {
			return err
		}
	}
	if err := disk.Sync(); err != nil {
		return err
	}
	for _, w := range []struct {
		part gpt.Partition
		file Artifact
	}{{target.Root, r.Root}, {target.Verity, r.Verity}} {
		if err := writePartition(ctx, disk, w.part, w.file, fetch); err != nil {
			return err
		}
	}
	if err := disk.Sync(); err != nil {
		return err
	}
	label := ImageID + "_" + r.Version
	for _, w := range []struct {
		part gpt.Partition
		file Artifact
	}{{target.Root, r.Root}, {target.Verity, r.Verity}} {
		if err := gpt.Rename(disk, w.part.UUID, w.file.UUID, label); err != nil {
			return err
		}
	}
	if err := disk.Sync(); err != nil {
		return err
	}
	return writeImage(ctx, esp, r, fetch)
}

// removeImages removes from the ESP at esp every kernel image of bare metal's but booted's.
func removeImages(esp, booted string) error {
	images, err := Images(esp)
	if err != nil {
		return err
	}
	for _, i := range images {
		if i.Version == booted {
			continue
		}
		if err := os.Remove(filepath.Join(esp, Linux, i.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return syncDir(filepath.Join(esp, Linux))
}

// writePartition writes the release's file a into the partition p of disk.
func writePartition(ctx context.Context, disk Disk, p gpt.Partition, a Artifact, fetch Fetch) error {
	body, size, err := fetch(ctx, a.Name)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }() // read to its end or given up on
	// A length the server says is refused before a byte is written; one it does not say, as it
	// goes.
	if size > p.Size() {
		return fmt.Errorf("update: %s is %d bytes, and its partition holds %d", a.Name, size, p.Size())
	}
	return copyChecked(io.NewOffsetWriter(disk, p.Offset()), body, p.Size(), a)
}

// copyChecked copies body, the release's file a, into w: no more than limit bytes, and what it
// wrote hashing to a's digest.
func copyChecked(w io.Writer, body io.Reader, limit int64, a Artifact) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(body, limit+1)) // mutate-exempt: any bound past the limit reads enough to find a longer file
	if err != nil {
		return fmt.Errorf("update: writing %s: %w", a.Name, err)
	}
	if n > limit {
		return fmt.Errorf("update: %s is more than the %d bytes it has room for", a.Name, limit)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("update: %s is %s, and %s says %s", a.Name, got, SumsFile, a.SHA256)
	}
	return nil
}

// writeImage writes the release's kernel image into the ESP at esp, counted, as Write says.
func writeImage(ctx context.Context, esp string, r Release, fetch Fetch) error {
	dir := filepath.Join(esp, Linux)
	partial := filepath.Join(dir, "."+r.UKI.Name+".partial")
	if err := download(ctx, fetch, r.UKI, partial); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := checkImage(partial, r.Version, r.Root.UUID, r.Verity.UUID); err != nil {
		_ = os.Remove(partial)
		return err
	}
	counted := ImageID + "_" + r.Version + "+" + strconv.Itoa(Tries) + ".efi"
	if err := os.Rename(partial, filepath.Join(dir, counted)); err != nil { // mutate-exempt: a rename that worked is synced or not, which only a power cut tells apart
		return err
	}
	return syncDir(dir)
}

// download writes the release's file a to path, synced: no more than a kernel image may be, and
// what it wrote hashing to a's digest.
func download(ctx context.Context, fetch Fetch, a Artifact, path string) error {
	body, _, err := fetch(ctx, a.Name)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }() // read to its end or given up on
	// Root's alone, as the ESP's mount makes every file on it whatever this says.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = copyChecked(f, body, maxImage, a)
	if err == nil { // mutate-exempt: a file synced or not reads the same until a power cut
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil { // mutate-exempt: a close that fails after every write passed is not one a test can make
		err = cerr
	}
	return err
}

// syncDir makes what was renamed and removed in dir stay so across a power cut.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil { // mutate-exempt: a directory synced or not holds the same files until a power cut
		return err
	}
	defer func() { _ = d.Close() }() // read-only
	return d.Sync()
}
