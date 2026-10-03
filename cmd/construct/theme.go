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

// themeStamp records, in each directory theme writes, each file it wrote and its digest: how it
// tells its own copy, which it may refresh, from a theme the user put there.
const themeStamp = ".construct-theme"

// themeCmd copies the image's GTK themes where apps take them from: Orchis's light GTK 4 theme
// (which carries the dark styles) into ~/.config/gtk-4.0, the only place libadwaita apps read,
// and each Orchis variant's GTK 3 theme into ~/.themes/<name>/gtk-3.0, where GTK 3 finds a theme
// even inside a Flatpak, whose own /usr has none of the image's and whose XDG_DATA_HOME is the
// app's. Copies, not links: a Flatpak sees both directories (the image's read-only override) but
// not the host's /usr, where links would point. Run at every login, it brings an updated image's
// themes along, and leaves a directory alone once the user has put their own gtk.css there.
func themeCmd(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("theme", flag.ContinueOnError)
	themes := fl.String("themes", "/usr/share/themes", "where the image's themes are")
	home := fl.String("home", "", "the user's home directory (default: $HOME)")
	config := fl.String("config", "", "the user's configuration directory (default: $XDG_CONFIG_HOME)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *home == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*home = dir
	}
	if *config == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*config = dir
	}
	copies := [][2]string{{filepath.Join(*themes, "Orchis-Construct-Light/gtk-4.0"), filepath.Join(*config, "gtk-4.0")}}
	for _, name := range []string{"Orchis-Construct-Dark", "Orchis-Construct-Light"} {
		copies = append(copies, [2]string{filepath.Join(*themes, name, "gtk-3.0"), filepath.Join(*home, ".themes", name, "gtk-3.0")})
	}
	for _, c := range copies {
		synced, err := syncTheme(c[0], c[1])
		if err != nil {
			return err
		}
		if !synced {
			fmt.Fprintf(stdout, "%s holds the user's own theme: left as it is\n", c[1])
		}
	}
	return nil
}

// syncTheme makes dst a copy of src, unless dst's gtk.css is not one theme wrote (absent counts as
// its own: there is nothing to keep). It reports whether it copied.
func syncTheme(src, dst string) (bool, error) {
	stamp, err := readThemeStamp(filepath.Join(dst, themeStamp))
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
	return true, writeThemeStamp(filepath.Join(dst, themeStamp), written)
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
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".construct-*")
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

// readThemeStamp reads "<digest> <path>" lines; a missing stamp is an empty one.
func readThemeStamp(path string) (map[string]string, error) {
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

func writeThemeStamp(path string, stamp map[string]string) error {
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
