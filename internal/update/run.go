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

// DefaultSource is where a stick's release is mounted (spindrift-update@, systemd-sysupdate's
// file:// source), which install reads by default.
const DefaultSource = "/run/spindrift/update"

// Size limits of what is read before its signature is checked.
const (
	maxSums      = 1 << 20
	maxSignature = 64 << 10
)

// Newest is the newest release under source - source itself when it holds SHA256SUMS, else each
// directory in it that does - and how its files are read. Every release there is checked against
// keys before any is chosen: a stick that carries one signed by another key is refused whole, not
// passed over for its neighbour.
func Newest(ctx context.Context, source string, keys openpgp.KeyRing) (Release, Fetch, error) {
	dirs, err := releaseDirs(source)
	if err != nil {
		return Release{}, nil, err
	}
	var best Release
	var bestDir string
	for _, dir := range dirs {
		r, err := ReadRelease(ctx, dirFetch(dir), keys)
		if err != nil {
			return Release{}, nil, fmt.Errorf("%s: %w", dir, err)
		}
		if bestDir == "" || Compare(r.Version, best.Version) > 0 {
			best, bestDir = r, dir
		}
	}
	return best, dirFetch(bestDir), nil
}

// releaseDirs are the directories under source that hold a release.
func releaseDirs(source string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(source, SumsFile)); err == nil {
		return []string{source}, nil
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, fmt.Errorf("update: the release source: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		dir := filepath.Join(source, e.Name())
		if _, err := os.Stat(filepath.Join(dir, SumsFile)); e.IsDir() && err == nil {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("update: %s holds no release (no %s)", source, SumsFile)
	}
	return dirs, nil
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

// ReadRelease is the release fetch reads: SHA256SUMS, checked against its signature by keys
// before anything of it is read, and the files it names.
func ReadRelease(ctx context.Context, fetch Fetch, keys openpgp.KeyRing) (Release, error) {
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
