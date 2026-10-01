package update

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

// ImageID is the desktop's IMAGE_ID in os-release, and what its root slots' labels, its kernel
// images' names and a release's files start with: what a version of it is matched by.
const ImageID = "spin-desktop"

// What a release holds besides its files: their digests, as sha256sum(1) writes them, and a
// detached OpenPGP signature of that list (gpg --detach-sign) by the key the machine's root trusts.
const (
	SumsFile      = "SHA256SUMS"
	SignatureFile = "SHA256SUMS.sig"
)

// KeyringPath is where the root carries the public key a release's signature is checked against,
// armored or binary, relative to the root. The private half is the person's, offline: it never is
// in a build, an image or this repository.
const KeyringPath = "usr/share/spin-desktop/release.pgp"

// Names are the files of version's release whose root hash gives its slot the partition UUIDs
// root and verity: the slot's root and hash tree, named with the version and the UUID each
// partition is given (systemd-sysupdate's @v and @u), and the kernel image, which systemd-boot is
// to count three tries of on the ESP.
func Names(version, root, verity string) (rootFile, verityFile, uki string) {
	prefix := ImageID + "_" + version
	return prefix + "_" + root + ".root.raw", prefix + "_" + verity + ".verity.raw", prefix + ".efi"
}

// ParseKeyring is the public keys in raw, armored (gpg --armor --export) or binary (gpg --export).
func ParseKeyring(raw []byte) (openpgp.EntityList, error) {
	var keys openpgp.EntityList
	var err error
	if block, aerr := armor.Decode(bytes.NewReader(raw)); aerr == nil {
		keys, err = openpgp.ReadKeyRing(block.Body)
	} else {
		keys, err = openpgp.ReadKeyRing(bytes.NewReader(raw))
	}
	if err != nil {
		return nil, fmt.Errorf("update: the release keyring: %w", err)
	}
	if len(keys) == 0 {
		return nil, errors.New("update: the release keyring holds no key")
	}
	return keys, nil
}

// Verify is nil when sig, a detached signature, armored or binary, is one of keys' over sums.
func Verify(keys openpgp.KeyRing, sums, sig []byte) error {
	check := openpgp.CheckDetachedSignature
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN PGP SIGNATURE")) {
		check = openpgp.CheckArmoredDetachedSignature
	}
	if _, err := check(keys, bytes.NewReader(sums), bytes.NewReader(sig), nil); err != nil {
		return fmt.Errorf("update: %s is not signed by the key this machine trusts: %w", SumsFile, err)
	}
	return nil
}

// WriteSums writes SHA256SUMS over every other file in dir, but its signature: what gpg then
// signs (gpg --detach-sign SHA256SUMS) to make the directory a release.
func WriteSums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var sums bytes.Buffer
	for _, e := range entries {
		if e.Name() == SumsFile || e.Name() == SignatureFile {
			continue
		}
		if !e.Type().IsRegular() {
			return fmt.Errorf("update: %s in %s is not a file", e.Name(), dir)
		}
		sum, err := fileSum(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, e.Name())
	}
	return os.WriteFile(filepath.Join(dir, SumsFile), sums.Bytes(), 0o644)
}

// fileSum is the hex SHA-256 of the file at path, as sha256sum(1) prints it.
func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sumLine is a line of SHA256SUMS: a digest, a space, a space or sha256sum's '*' for a file read
// as binary, and a file's name, which has no directory.
var sumLine = regexp.MustCompile(`^([0-9a-f]{64}) [ *]([^/\s]+)$`)

// ParseSums is the digest of each file SHA256SUMS names, by name. A line that is not one, and a
// name twice, are refused: the list is what the signature vouches for, read one way only.
func ParseSums(raw []byte) (map[string]string, error) {
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		m := sumLine.FindStringSubmatch(sc.Text())
		if m == nil {
			return nil, fmt.Errorf("update: %q is not a line of %s", sc.Text(), SumsFile)
		}
		if _, twice := sums[m[2]]; twice {
			return nil, fmt.Errorf("update: %s names %s twice", SumsFile, m[2])
		}
		sums[m[2]] = m[1]
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

// Artifact is one file of a release: its name, its digest, and for a partition's the UUID its
// name gives the partition.
type Artifact struct {
	Name, SHA256, UUID string
}

// Release is the version a release is of, and its three files a machine writes.
type Release struct {
	Version           string
	Root, Verity, UKI Artifact
}

// The names of a release's files, as Names makes them. A version is what cmd/image usb writes - a
// UTC time, 20261001.134321 - and anything systemd orders: no underscore, which ends it, and no
// '+', which systemd-boot reads as the start of a kernel image's tries.
const (
	versionPattern = `([0-9][0-9A-Za-z.~^-]*)`
	uuidPattern    = `([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`
)

var (
	rootName   = regexp.MustCompile(`^` + ImageID + `_` + versionPattern + `_` + uuidPattern + `\.root\.raw$`)
	verityName = regexp.MustCompile(`^` + ImageID + `_` + versionPattern + `_` + uuidPattern + `\.verity\.raw$`)
	ukiName    = regexp.MustCompile(`^` + ImageID + `_` + versionPattern + `\.efi$`)
)

// Find is the release sums lists: exactly one root, one hash tree and one kernel image, all of one
// version. Anything else it lists is no business of a machine's.
func Find(sums map[string]string) (Release, error) {
	var r Release
	found := map[string]*Artifact{}
	versions := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(sums)) {
		for _, kind := range []struct {
			re   *regexp.Regexp
			into *Artifact
			what string
		}{{rootName, &r.Root, "root"}, {verityName, &r.Verity, "hash tree"}, {ukiName, &r.UKI, "kernel image"}} {
			m := kind.re.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			if found[kind.what] != nil {
				return Release{}, fmt.Errorf("update: %s names two of a release's %s: %s and %s", SumsFile, kind.what, found[kind.what].Name, name)
			}
			*kind.into = Artifact{Name: name, SHA256: sums[name]}
			if len(m) > 2 {
				kind.into.UUID = m[2]
			}
			found[kind.what] = kind.into
			versions[m[1]] = true
		}
	}
	var missing []string
	for _, what := range []string{"root", "hash tree", "kernel image"} {
		if found[what] == nil {
			missing = append(missing, what)
		}
	}
	switch {
	case len(missing) > 0:
		return Release{}, fmt.Errorf("update: %s names no %s", SumsFile, strings.Join(missing, ", no "))
	case len(versions) != 1:
		return Release{}, fmt.Errorf("update: %s names files of %d versions, not one", SumsFile, len(versions))
	case r.Root.UUID == r.Verity.UUID:
		return Release{}, errors.New("update: the root and its hash tree are to be one partition")
	}
	for v := range versions {
		r.Version = v
	}
	return r, nil
}

// ImageVersion is the IMAGE_VERSION an os-release says: the version a root is.
func ImageVersion(osRelease string) (string, error) {
	for l := range strings.Lines(osRelease) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "IMAGE_VERSION="); ok && v != "" {
			return strings.Trim(v, `"`), nil
		}
	}
	return "", errors.New("update: the root's os-release has no IMAGE_VERSION")
}
