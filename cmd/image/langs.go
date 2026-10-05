package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

// wolfiIndex is the index of the Wolfi repository image/apko.yaml builds from.
const wolfiIndex = "https://packages.wolfi.dev/os/x86_64/APKINDEX.tar.gz"

// checkLangs fails when a package in the root has translations in a -lang package that the
// root does not install. Wolfi and recipes/ both split a package's catalogs into <name>-lang,
// which nothing installs unless image/apko.yaml names it; without them GNOME Shell, GTK and
// Files spoke English whatever the person chose - while AGENTS.md promises every language the
// packages ship. It runs after apko in task rootfs, so a package added without its
// translations fails the build that added it.
func checkLangs(rootTar string) error {
	installed, err := installedPackages(rootTar)
	if err != nil {
		return err
	}
	available := map[string]bool{}
	for _, idx := range []string{wolfiIndex, "packages/x86_64/APKINDEX.tar.gz"} {
		if err := indexPackages(idx, available); err != nil {
			return fmt.Errorf("%s: %w", idx, err)
		}
	}
	if missing := missingLangs(installed, available); len(missing) > 0 {
		return fmt.Errorf("the root lacks the translations of what it installs; add to image/apko.yaml:\n    - %s", strings.Join(missing, "\n    - "))
	}
	return nil
}

// missingLangs are the <name>-lang packages available for an installed package and not
// installed themselves, sorted.
func missingLangs(installed, available map[string]bool) []string {
	var out []string
	for p := range installed {
		if l := p + "-lang"; available[l] && !installed[l] {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// installedPackages are the names in the root's apk database.
func installedPackages(rootTar string) (map[string]bool, error) {
	names := map[string]bool{}
	found := false
	err := tarLayer{file: rootTar}.walk(func(h *tar.Header, r io.Reader) error {
		if h.Name != "usr/lib/apk/db/installed" && h.Name != "lib/apk/db/installed" {
			return nil
		}
		found = true
		return packageNames(r, names)
	})
	if err == nil && !found {
		err = fmt.Errorf("%s has no lib/apk/db/installed", rootTar)
	}
	return names, err
}

// indexPackages adds the names in an APKINDEX.tar.gz, a path or an https URL, to names.
func indexPackages(src string, names map[string]bool) error {
	var r io.ReadCloser
	if strings.HasPrefix(src, "https://") {
		resp, err := http.Get(src)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("%s", resp.Status)
		}
		r = resp.Body
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		r = f
	}
	defer r.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	// The index is a gzip stream of the signature's tar and then the index's: read them as one.
	gz.Multistream(true)
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no APKINDEX in it")
		}
		if err != nil {
			return err
		}
		if h.Name == "APKINDEX" {
			return packageNames(tr, names)
		}
	}
}

// packageNames adds the packages of an apk database or index to names.
func packageNames(r io.Reader, names map[string]bool) error {
	db, err := readAPKDB(r)
	for _, p := range db {
		names[p.Name] = true
	}
	return err
}
