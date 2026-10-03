package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// gtk4Stamp records, in the user's gtk-4.0 directory, each file gtk4 wrote and its digest: how
// it tells its own copy, which it may refresh, from a theme the user put there.
const gtk4Stamp = ".drift-gtk4"

// gtk4Cmd copies the image's GTK 4 theme into the user's ~/.config/gtk-4.0, the only place
// libadwaita apps take styles from. Copies, not links: a Flatpak sees that directory (the image's
// read-only override) but not the host's /usr, where links would point. Run at every login, it
// brings an updated image's theme along, and leaves the directory alone once the user has put
// their own gtk.css there.
func gtk4Cmd(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("gtk4", flag.ContinueOnError)
	from := fl.String("from", "/usr/share/themes/Orchis-Teal-Light/gtk-4.0", "the theme's gtk-4.0 directory")
	to := fl.String("to", "", "the user's gtk-4.0 directory (default: $XDG_CONFIG_HOME/gtk-4.0)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*to = filepath.Join(dir, "gtk-4.0")
	}
	synced, err := syncGTK4(*from, *to)
	if err != nil {
		return err
	}
	if !synced {
		fmt.Fprintf(stdout, "%s holds the user's own theme: left as it is\n", *to)
	}
	return nil
}

// syncGTK4 makes dst a copy of src, unless dst's gtk.css is not one gtk4 wrote (absent counts as
// its own: there is nothing to keep). It reports whether it copied.
func syncGTK4(src, dst string) (bool, error) {
	stamp, err := readGTK4Stamp(filepath.Join(dst, gtk4Stamp))
	if err != nil {
		return false, err
	}
	own, err := ownedByStamp(filepath.Join(dst, "gtk.css"), stamp["gtk.css"])
	if err != nil || !own {
		return false, err
	}

	written := map[string]string{}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		sum, err := copyFile(path, target)
		if err != nil {
			return err
		}
		written[filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return false, err
	}
	// What an earlier image's theme had and this one has not.
	for rel, sum := range stamp {
		if _, ok := written[rel]; ok {
			continue
		}
		path := filepath.Join(dst, filepath.FromSlash(rel))
		if own, _ := ownedByStamp(path, sum); own {
			_ = os.Remove(path)
		}
	}
	return true, writeGTK4Stamp(filepath.Join(dst, gtk4Stamp), written)
}

// ownedByStamp is true when path is absent, or a regular file whose digest is want.
func ownedByStamp(path, want string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || want == "" {
		return false, nil
	}
	got, err := fileSum(path)
	return got == want, err
}

// copyFile writes src to dst through a temporary file renamed into place, so an app starting
// meanwhile reads the old file or the new one, never half of one. It returns the digest.
func copyFile(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".drift-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// A link or directory the user left at dst is theirs to keep; rename does not replace those.
	if info, err := os.Lstat(dst); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", dst)
	}
	return hex.EncodeToString(h.Sum(nil)), os.Rename(tmp.Name(), dst)
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readGTK4Stamp reads "<digest> <path>" lines; a missing stamp is an empty one.
func readGTK4Stamp(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stamp := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, rel, ok := strings.Cut(sc.Text(), " ")
		if ok {
			stamp[rel] = sum
		}
	}
	return stamp, sc.Err()
}

func writeGTK4Stamp(path string, stamp map[string]string) error {
	rels := make([]string, 0, len(stamp))
	for rel := range stamp {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var b strings.Builder
	for _, rel := range rels {
		fmt.Fprintf(&b, "%s %s\n", stamp[rel], rel)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
