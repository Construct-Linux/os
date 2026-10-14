package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/gpt"
)

// The UUIDs of the slot a test release is for.
const (
	newRoot   = "0123abcd-0000-1111-2222-333344445555"
	newVerity = "89abcdef-6666-7777-8888-9999aaaabbbb"
)

func sumOf(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// The release on a stick is read from its root, checked against the keys before anything else of
// it is read: one signed by another key is refused.
func TestAReleaseOfAnotherKeyIsRefused(t *testing.T) {
	dir, _ := release(t, "1.1", newRoot, newVerity, signer(), 0xb)
	r, _, err := readSource(t.Context(), dir, trusted(t))
	if err != nil || r.Version != "1.1" {
		t.Fatalf("took %+v, %v", r, err)
	}
	other, _ := release(t, "1.3", newRoot, newVerity, otherKey(), 0xb)
	if _, _, err := readSource(t.Context(), other, trusted(t)); err == nil {
		t.Fatal("a release of another key was taken")
	}
}

// A source that holds no release is an error, not a quiet nothing: the stick plugged in is not
// what the person thinks it is.
func TestASourceWithNoReleaseIsAnError(t *testing.T) {
	if _, _, err := readSource(t.Context(), t.TempDir(), trusted(t)); err == nil {
		t.Error("an empty source was taken")
	}
}

// A partition is written from its xz-compressed file whole: decompressed into the partition, held
// to the digest of the bytes as published, and to the room the partition has.
func TestAPartitionIsWrittenDecompressedAndHeldToItsDigestAndRoom(t *testing.T) {
	const room = 64 << 10
	body := bytes.Repeat([]byte{7}, room)
	disk := func(t *testing.T) *os.File {
		f, err := os.Create(filepath.Join(t.TempDir(), "disk"))
		must(t, err)
		t.Cleanup(func() { _ = f.Close() })
		must(t, f.Truncate(1<<20))
		return f
	}
	p := gpt.Partition{First: 8, Last: 8 + room/gpt.Sector - 1}
	fetchOf := func(b []byte) Fetch {
		return func(context.Context, string) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
		}
	}
	good := xzOf(t, body)
	d := disk(t)
	// The installer's progress reaches the whole file, as fetched, and says which.
	var last, total int64
	progress := func(file string, done, all int64) {
		if file != "root" || done < last {
			t.Errorf("progress went %s %d after %d", file, done, last)
		}
		last, total = done, all
	}
	if err := writePartition(t.Context(), d, p, Artifact{Name: "root", SHA256: sumOf(good)}, fetchOf(good), progress); err != nil {
		t.Fatalf("a partition of exactly its room: %v", err)
	}
	if last != int64(len(good)) || total != int64(len(good)) {
		t.Errorf("progress ended at %d of %d; the file is %d", last, total, len(good))
	}
	got := make([]byte, room)
	_, err := d.ReadAt(got, p.Offset())
	must(t, err)
	if !bytes.Equal(got, body) {
		t.Error("the partition does not hold the file decompressed")
	}

	corrupt := bytes.Clone(good)
	corrupt[len(corrupt)/2] ^= 0xff
	longer := xzOf(t, append(bytes.Clone(body), 7))
	for name, tc := range map[string]struct {
		file []byte
		sum  string
		says string
	}{
		"corrupted xz":              {corrupt, sumOf(corrupt), "root"},
		"another file's digest":     {good, sumOf(body), "SHA256SUMS says"},
		"a byte more than its room": {longer, sumOf(longer), "more than"},
		"not xz":                    {body, sumOf(body), "not xz"},
	} {
		err := writePartition(t.Context(), disk(t), p, Artifact{Name: "root", SHA256: tc.sum}, fetchOf(tc.file), nil)
		if err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.says)
		}
	}
}

// An install cancelled while a partition is written says it was cancelled, not that the release's
// file is not xz: the context kills xz, and the kill is not the file's fault.
func TestACancelledWriteSaysSo(t *testing.T) {
	body := bytes.Repeat([]byte{7}, 64<<10)
	good := xzOf(t, body)
	ctx, cancel := context.WithCancel(t.Context())
	// The first read of the file cancels, as a person closing the installer mid-write would.
	fetch := func(context.Context, string) (io.ReadCloser, int64, error) {
		return io.NopCloser(&cancelling{r: bytes.NewReader(good), cancel: cancel}), int64(len(good)), nil
	}
	disk, err := os.Create(filepath.Join(t.TempDir(), "disk"))
	must(t, err)
	defer disk.Close()
	must(t, disk.Truncate(1<<20))
	p := gpt.Partition{First: 8, Last: 8 + int64(len(body))/gpt.Sector - 1}
	err = writePartition(ctx, disk, p, Artifact{Name: "root", SHA256: sumOf(good)}, fetch, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled write: %v, want %v", err, context.Canceled)
	}
}

// cancelling is a reader that cancels on its first read and then reads on.
type cancelling struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c *cancelling) Read(p []byte) (int, error) {
	c.cancel()
	return c.r.Read(p)
}

// What is copied from a release is held to its digest and to the room it has: a file of exactly
// that room is written whole, and one a byte longer is not.
func TestAFileIsHeldToItsRoom(t *testing.T) {
	body := bytes.Repeat([]byte{7}, 4096)
	a := Artifact{Name: "root", SHA256: sumOf(body)}
	var w bytes.Buffer
	if err := copyChecked(&w, bytes.NewReader(body), int64(len(body)), a); err != nil || !bytes.Equal(w.Bytes(), body) {
		t.Errorf("a file of exactly its room: %v, %d bytes written", err, w.Len())
	}
	longer := append(bytes.Clone(body), 7)
	if err := copyChecked(io.Discard, bytes.NewReader(longer), int64(len(body)), Artifact{Name: "root", SHA256: sumOf(longer)}); err == nil {
		t.Error("a file a byte longer than its room was taken")
	}
	if err := copyChecked(io.Discard, bytes.NewReader(body[1:]), int64(len(body)), a); err == nil {
		t.Error("a file that is not its digest's was taken")
	}
}

// What is read before its signature is checked is read whole up to its bound, and refused past it.
func TestTheListIsReadUpToItsBound(t *testing.T) {
	fetch := func(body []byte) Fetch {
		return func(context.Context, string) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(body)), -1, nil
		}
	}
	whole := bytes.Repeat([]byte{'x'}, 100)
	if got, err := readSmall(t.Context(), fetch(whole), SumsFile, 100); err != nil || !bytes.Equal(got, whole) {
		t.Errorf("a list of its bound read as %d bytes, %v", len(got), err)
	}
	if got, err := readSmall(t.Context(), fetch(append(whole, 'x')), SumsFile, 100); err == nil {
		t.Errorf("a list past its bound read as %d bytes", len(got))
	}
}

// A release's files are read from its directory alone: a name SHA256SUMS gives with a directory in
// it is refused before anything is opened.
func TestAReleaseIsReadFromItsOwnDirectoryOnly(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "release")
	write(t, filepath.Join(parent, "secret"), "not the release's")
	write(t, filepath.Join(dir, "SHA256SUMS"), "")
	for _, name := range []string{"../secret", "sub/file", "/etc/passwd"} {
		if rc, _, err := dirFetch(dir)(t.Context(), name); err == nil {
			_ = rc.Close()
			t.Errorf("%q was read", name)
		}
	}
}
