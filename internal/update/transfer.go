package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spin-stack/spindrift/internal/gpt"
)

// Disk is the disk an install writes, open to be read and written.
type Disk interface {
	gpt.Disk
	Sync() error
}

// Fetch opens the file of the release called name, and says how long it is when it knows (-1
// when it does not).
type Fetch func(ctx context.Context, name string) (io.ReadCloser, int64, error)

// maxImage is the most a kernel image may be: the ESP holds two, and is 512 MiB.
const maxImage = 200 << 20

// writePartition writes the release's file a - an xz-compressed partition image - into the
// partition p of disk, decompressed: the compressed bytes as read hash to a's digest, and what they
// decompress to fits p. Bytes are written before the digest is known, so a caller leaves the
// partition labelled Empty until this returns nil: what it wrote is nothing that boots otherwise.
func writePartition(ctx context.Context, disk Disk, p gpt.Partition, a Artifact, fetch Fetch) error {
	body, _, err := fetch(ctx, a.Name)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }() // read to its end or given up on
	h := sha256.New()
	// xz(1), with liblzma's threads: a release is compressed in blocks (xz -T0), which it
	// decompresses in parallel. Go's pure xz reader took 7m43s for a 1.4 GB root that xz -T0
	// decompresses in 3s.
	xz := exec.CommandContext(ctx, "xz", "--decompress", "--stdout", "--threads=0")
	xz.Stdin = io.TeeReader(body, h)
	var stderr bytes.Buffer
	xz.Stderr = &stderr
	out, err := xz.StdoutPipe()
	if err != nil {
		return err
	}
	if err := xz.Start(); err != nil {
		return fmt.Errorf("update: decompressing %s: %w", a.Name, err)
	}
	limit := p.Size()
	n, copyErr := io.Copy(io.NewOffsetWriter(disk, p.Offset()), io.LimitReader(out, limit+1)) // mutate-exempt: any bound past the limit reads enough to find a longer image
	if copyErr != nil || n > limit {
		_ = xz.Process.Kill()
	}
	xzErr := xz.Wait()
	switch {
	case copyErr != nil:
		return fmt.Errorf("update: writing %s: %w", a.Name, copyErr)
	case n > limit:
		return fmt.Errorf("update: %s decompresses to more than the %d bytes its partition holds", a.Name, limit)
	case xzErr != nil:
		return fmt.Errorf("update: %s is not xz that decompresses: %w: %s", a.Name, xzErr, strings.TrimSpace(stderr.String()))
	}
	// Whatever xz left unread is in the digest too.
	if _, err := io.Copy(h, body); err != nil {
		return fmt.Errorf("update: reading %s: %w", a.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("update: %s is %s, and %s says %s", a.Name, got, SumsFile, a.SHA256)
	}
	return nil
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
