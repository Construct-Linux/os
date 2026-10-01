package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/spin-stack/spin-desktop/internal/gpt"
)

// System is what an update and a boot check ask of the machine's systemd: whether a unit is
// active, and the reboot.
type System interface {
	Active(ctx context.Context, unit string) (bool, error)
	Reboot(ctx context.Context) error
}

// DefaultSource is where an update stick's release is mounted for spin-desktop update to read.
const DefaultSource = "/run/spin-desktop/update"

// Options is where an update reads and writes on a machine.
type Options struct {
	// Keys are what a release's signature is checked against; OSRelease the running root's
	// os-release, whose IMAGE_VERSION is the booted version.
	Keys      openpgp.KeyRing
	OSRelease string
	// Source is the directory a release is read from: one release, or a directory of them.
	Source string
	// Root is the running root's device, which FindRoot finds under Sys; OpenDisk opens the disk
	// it answers, to be read and written.
	Sys      string
	Root     uint64
	OpenDisk func(path string) (Disk, error)
	// ESP is where the ESP the machine booted from is mounted.
	ESP string
	Log *slog.Logger
}

// ErrOlder is a release older than the version the machine runs: never written. Updates come on
// sticks a person carries; an old one plugged in is not a way back - the boot menu is.
var ErrOlder = errors.New("update: the release is older than this machine")

// Result is what an update did: the version it wrote, if it wrote one, and the version a reboot
// moves the machine to, if any.
type Result struct {
	Written, Pending string
}

// Run is one look at the source: the newest release there, its signature checked, written into the
// slot the machine did not boot when it is newer than both the booted one and the one there, its
// kernel image counted beside the booted one's. It never reboots: the person does, when they will,
// and systemd-boot falls back from a version that does not come up.
func Run(ctx context.Context, o Options) (Result, error) {
	booted, err := imageVersion(o.OSRelease)
	if err != nil {
		return Result{}, err
	}
	r, fetch, err := Newest(ctx, o.Source, o.Keys)
	if err != nil {
		return Result{}, err
	}
	dev, err := FindRoot(o.Sys, o.Root)
	if err != nil {
		return Result{}, err
	}
	disk, err := o.OpenDisk(dev.Disk)
	if err != nil {
		return Result{}, err
	}
	if closer, ok := disk.(io.Closer); ok {
		defer func() { _ = closer.Close() }() // every write is synced where it is made
	}
	table, err := gpt.Read(disk)
	if err != nil {
		return Result{}, fmt.Errorf("update: %s: %w", dev.Disk, err)
	}
	slots, err := Slots(table)
	if err != nil {
		return Result{}, err
	}
	bootedSlot, err := Booted(slots, dev.Starts, booted)
	if err != nil {
		return Result{}, err
	}
	var res Result
	log := o.Log.With("running", booted, "release", r.Version)
	switch c := Compare(r.Version, booted); {
	case c < 0:
		return Result{}, fmt.Errorf("%w: refusing %s, which is older than %s, the version it runs", ErrOlder, r.Version, booted)
	case c == 0:
		log.Info("this machine runs the release")
	case holds(slots, r.Version):
		log.Info("the release is written already")
	default:
		target, err := Target(slots, bootedSlot)
		if err != nil {
			return Result{}, err
		}
		log.Info("writing the release", "slot", target.Root.UUID, "was", target.Label)
		if err := Write(ctx, disk, o.ESP, target, booted, r, fetch); err != nil {
			return Result{}, err
		}
		if table, err = gpt.Read(disk); err != nil {
			return Result{}, err
		}
		if slots, err = Slots(table); err != nil {
			return Result{}, err
		}
		res.Written = r.Version
		log.Info("written: systemd-boot tries it at the next boot, and falls back to this one if it does not come up", "tries", Tries)
	}
	images, err := Images(o.ESP)
	if err != nil {
		return Result{}, err
	}
	res.Pending = Pending(slots, images, booted)
	return res, nil
}

// holds is whether one of slots holds version.
func holds(slots []Slot, version string) bool {
	for _, s := range slots {
		if s.Version == version {
			return true
		}
	}
	return false
}

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

// imageVersion is the IMAGE_VERSION the os-release at path says.
func imageVersion(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return ImageVersion(string(raw))
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
