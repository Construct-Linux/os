package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// docsLeftOut are the -doc packages of commands the root leaves out, each with the reason.
var docsLeftOut = map[string]string{
	"gtk-4-doc": "GTK's API reference in HTML, 93 MB, and no manual page in it",
}

// checkDocs fails when a package in the root ships a command and its manual is in a -doc
// package the root does not install. Wolfi splits manual pages into <name>-doc, which apko
// installs only when image/apko.yaml names it: the root had man-db's index and 42 pages, and
// `man top` found neither man nor the page. It runs after apko in task rootfs, beside
// check-langs, so a command added without its manual fails the build that added it.
func checkDocs(rootTar string) error {
	db, err := installedDB(rootTar)
	if err != nil {
		return err
	}
	available, err := availablePackages()
	if err != nil {
		return err
	}
	if missing := missingDocs(db, available); len(missing) > 0 {
		return fmt.Errorf("the root lacks the manuals of commands it installs; add to image/apko.yaml (or to docsLeftOut, with the reason):\n    - %s", strings.Join(missing, "\n    - "))
	}
	return nil
}

// missingDocs are the <name>-doc packages available for an installed package with a command,
// not installed themselves and not left out on purpose, sorted.
func missingDocs(db []apkPackage, available map[string]bool) []string {
	installed := map[string]bool{}
	for _, p := range db {
		installed[p.Name] = true
	}
	var out []string
	for _, p := range db {
		d := p.Name + "-doc"
		if available[d] && !installed[d] && docsLeftOut[d] == "" && hasCommand(p.Files) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// hasCommand reports whether any of a package's files is in a directory the PATH names.
func hasCommand(files []string) bool {
	for _, f := range files {
		switch path.Dir(f) {
		case "bin", "sbin", "usr/bin", "usr/sbin":
			return true
		}
	}
	return false
}
