package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// System is what a boot check asks of the machine's systemd: whether a unit is active, and the
// reboot.
type System interface {
	Active(ctx context.Context, unit string) (bool, error)
	Reboot(ctx context.Context) error
}

// Size limits of what is read before its signature is checked.
const (
	maxSums      = 1 << 20
	maxSignature = 64 << 10
)

// readSource is the release at source, a directory with SHA256SUMS at its root - the stick's
// construct-update partition - checked against keys before anything else of it is read, and how
// its files are read.
func readSource(ctx context.Context, source string, keys openpgp.KeyRing) (Release, Fetch, error) {
	fetch := dirFetch(source)
	r, err := readRelease(ctx, fetch, keys)
	if err != nil {
		return Release{}, nil, err
	}
	return r, fetch, nil
}

// dirFetch reads a release's files from dir.
func dirFetch(dir string) Fetch {
	return func(_ context.Context, name string) (io.ReadCloser, int64, error) {
		if filepath.Base(name) != name {
			return nil, 0, fmt.Errorf("update: %q is not a file of the release", name)
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, 0, fmt.Errorf("update: %w", err)
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, err
		}
		return f, fi.Size(), nil
	}
}

// readRelease is the release fetch reads: SHA256SUMS, checked against its signature by keys
// before anything of it is read, and the files it names.
func readRelease(ctx context.Context, fetch Fetch, keys openpgp.KeyRing) (Release, error) {
	sums, err := readSmall(ctx, fetch, SumsFile, maxSums)
	if err != nil {
		return Release{}, err
	}
	sig, err := readSmall(ctx, fetch, SignatureFile, maxSignature)
	if err != nil {
		return Release{}, err
	}
	if err := Verify(keys, sums, sig); err != nil {
		return Release{}, err
	}
	list, err := ParseSums(sums)
	if err != nil {
		return Release{}, err
	}
	return Find(list)
}

// readSmall is the whole of the release's file name, which is at most limit bytes.
func readSmall(ctx context.Context, fetch Fetch, name string, limit int64) ([]byte, error) {
	body, _, err := fetch(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()                   // read to its end or given up on
	raw, err := io.ReadAll(io.LimitReader(body, limit+1)) // mutate-exempt: any bound past the limit reads enough to find a longer file
	if err != nil {
		return nil, fmt.Errorf("update: reading %s: %w", name, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("update: %s is more than %d bytes", name, limit)
	}
	return raw, nil
}

// ReadKeyring is the keyring at path, which the root carries at KeyringPath.
func ReadKeyring(path string) (openpgp.EntityList, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("update: this image trusts no release: %w", err)
	}
	if err != nil {
		return nil, err
	}
	return ParseKeyring(raw)
}
