package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// packages/ between builds. The release builder starts from the cache (infra's packages bucket)
// and builds only the recipes whose package the cache lacks, in the order their build environments
// need each other; before the cache goes back, every apk no recipe makes goes, so the cache holds
// what the recipes make and no history.

// recipe is what packages/ needs to know of a recipe: the package it makes, and the packages its
// build and its package need, some of which other recipes make.
type recipe struct {
	// File is the recipe's name, as task build takes it; Name its package's, which may differ.
	File, Name, Version string
	Epoch               int
	Needs               []string
}

// apk is the file a recipe's own package is: name-version-r<epoch>.apk.
func (r recipe) apk() string { return fmt.Sprintf("%s-%s-r%d.apk", r.Name, r.Version, r.Epoch) }

// readRecipe reads a melange recipe's package block (name, version, epoch, runtime dependencies)
// and its build environment's packages, line by line: no YAML library, and the recipes' layout is
// melange's own, two spaces a level.
func readRecipe(file string) (recipe, error) {
	f, err := os.Open(file)
	if err != nil {
		return recipe{}, err
	}
	defer f.Close()
	var r recipe
	var top, list string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			top, list = strings.TrimSuffix(trimmed, ":"), ""
			continue
		}
		key, value, isKey := strings.Cut(trimmed, ":")
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch {
		case top == "package" && indent == 2 && isKey && key == "name" && r.Name == "":
			r.Name = value
		case top == "package" && indent == 2 && isKey && key == "version" && r.Version == "":
			r.Version = value
		case top == "package" && indent == 2 && isKey && key == "epoch":
			r.Epoch, err = strconv.Atoi(value)
			if err != nil {
				return recipe{}, fmt.Errorf("%s: epoch %q", file, value)
			}
		case (top == "package" && indent == 4 && trimmed == "runtime:") || (top == "environment" && indent == 4 && trimmed == "packages:"):
			list = top
		case list != "" && indent == 6 && strings.HasPrefix(trimmed, "- "):
			// A constraint (name=version, name>=version) names the same package.
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if i := strings.IndexAny(name, "=<>~"); i > 0 {
				name = name[:i]
			}
			r.Needs = append(r.Needs, strings.Trim(name, `"'`))
		case indent <= 4:
			list = ""
		}
	}
	if err := s.Err(); err != nil {
		return recipe{}, err
	}
	if r.Name == "" || r.Version == "" {
		return recipe{}, fmt.Errorf("%s: no package name or version", file)
	}
	return r, nil
}

// missingInWaves are the recipes whose package is not in have, in waves: each recipe in the wave
// after the last of the missing recipes its build or package needs, so a wave's recipes build side
// by side. A need is another recipe's when it is that recipe's package or one of its subpackages
// (its name, then a dash).
func missingInWaves(recipes []recipe, have map[string]bool) [][]string {
	missing := map[string]recipe{}
	for _, r := range recipes {
		if !have[r.apk()] {
			missing[r.File] = r
		}
	}
	// The missing recipe that makes need: the longest package name need is, or starts a
	// subpackage of.
	makes := func(need string) string {
		best, bestLen := "", 0
		for file, r := range missing {
			if (need == r.Name || strings.HasPrefix(need, r.Name+"-")) && len(r.Name) > bestLen {
				best, bestLen = file, len(r.Name)
			}
		}
		return best
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	// wave is 0 for a recipe being visited: a cycle is cut there, and melange takes the package
	// from Wolfi or the cache meanwhile.
	wave := map[string]int{}
	var visit func(string) int
	visit = func(n string) int {
		if w, ok := wave[n]; ok {
			return w
		}
		wave[n] = 0
		w := 1
		for _, need := range missing[n].Needs {
			if m := makes(need); m != "" && m != n {
				w = max(w, visit(m)+1)
			}
		}
		wave[n] = w
		return w
	}
	var waves [][]string
	for _, n := range names {
		w := visit(n)
		for len(waves) < w {
			waves = append(waves, nil)
		}
		waves[w-1] = append(waves[w-1], n)
	}
	return waves
}

// pkgInfo is what an apk's .PKGINFO says of the recipe that made it: origin, the recipe's package
// name, and pkgver, the version and epoch it was built at. An apk is three gzip streams end to
// end - signature, control, data - each a tar, and only the last with an end marker, so a tar
// reader over the whole reads them as one and .PKGINFO, the control's, comes before any file.
func pkgInfo(path string) (origin, pkgver string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", "", fmt.Errorf("%s: no .PKGINFO: %w", path, err)
		}
		if h.Name != ".PKGINFO" {
			continue
		}
		sc := bufio.NewScanner(tr)
		for sc.Scan() {
			switch k, v, _ := strings.Cut(sc.Text(), " = "); k {
			case "origin":
				origin = v
			case "pkgver":
				pkgver = v
			}
		}
		if err := sc.Err(); err != nil {
			return "", "", fmt.Errorf("%s: %w", path, err)
		}
		if origin == "" || pkgver == "" {
			return "", "", fmt.Errorf("%s: .PKGINFO names no origin or pkgver", path)
		}
		return origin, pkgver, nil
	}
}

// unmade are the apks in dir that no recipe makes now: a package's builds at any version and
// epoch but its recipe's, and the packages of recipes that are gone. The recipes say what the
// cache holds, so no version is ordered: a version taken back is the recipe's again, not the
// newest build's. An apk that does not say what made it stops it: nothing is deleted on a guess.
func unmade(dir string, recipes []recipe) ([]string, error) {
	makes := make(map[string]string, len(recipes))
	for _, r := range recipes {
		makes[r.Name] = fmt.Sprintf("%s-r%d", r.Version, r.Epoch)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var old []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".apk") {
			continue
		}
		origin, pkgver, err := pkgInfo(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if makes[origin] != pkgver {
			old = append(old, e.Name())
		}
	}
	return old, nil
}

// packagesCmd is image packages missing, which prints the recipes whose package packages/ lacks,
// a line per wave that builds side by side, or image packages prune, which removes every apk no
// recipe makes now and prints the files it removed, for the index to be made again.
func packagesCmd(args []string) error {
	if len(args) != 1 || (args[0] != "missing" && args[0] != "prune") {
		return errors.New("packages missing|prune")
	}
	recipes, err := readRecipes("recipes")
	if err != nil {
		return err
	}
	dir := filepath.Join("packages", "x86_64")
	if args[0] == "prune" {
		old, err := unmade(dir, recipes)
		if err != nil {
			return err
		}
		for _, f := range old {
			if err := os.Remove(filepath.Join(dir, f)); err != nil {
				return err
			}
			fmt.Println(f)
		}
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, w := range missingInWaves(recipes, have) {
		fmt.Println(strings.Join(w, " "))
	}
	return nil
}

// readRecipes reads every recipe in dir, each named as task build takes it.
func readRecipes(dir string) ([]recipe, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var recipes []recipe
	for _, p := range paths {
		r, err := readRecipe(p)
		if err != nil {
			return nil, err
		}
		r.File = strings.TrimSuffix(filepath.Base(p), ".yaml")
		recipes = append(recipes, r)
	}
	return recipes, nil
}

// apkPackage is one package of an apk database or index: its name, its version, its origin (the
// package of the recipe that built it) and the files it installed, relative to the root (an index
// lists none).
type apkPackage struct {
	Name, Version, Origin string
	Files                 []string
}

// readAPKDB reads apk's database format, blocks of one-letter fields: P: names a package, V: is
// its version, o: its origin, F: a directory and the R: lines after it the files in it.
func readAPKDB(r io.Reader) ([]apkPackage, error) {
	var pkgs []apkPackage
	dir := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "P:"); ok {
			pkgs = append(pkgs, apkPackage{Name: v})
			dir = ""
			continue
		}
		if len(pkgs) == 0 {
			continue
		}
		p := &pkgs[len(pkgs)-1]
		switch {
		case strings.HasPrefix(line, "V:"):
			p.Version = line[2:]
		case strings.HasPrefix(line, "o:"):
			p.Origin = line[2:]
		case strings.HasPrefix(line, "F:"):
			dir = line[2:]
		case strings.HasPrefix(line, "R:"):
			p.Files = append(p.Files, filepath.Join(dir, line[2:]))
		}
	}
	return pkgs, sc.Err()
}

// overtaken are the packages in db that a recipe makes - the db's origin names the recipe's
// package - installed at a version other than the recipe's: Wolfi's, taken because apko installs
// the newest version across its repositories and Wolfi's climbed above the recipe's, and an
// override's whole point is what Wolfi's lacks. One line per package, sorted.
func overtaken(recipes []recipe, db []apkPackage) []string {
	makes := make(map[string]recipe, len(recipes))
	for _, r := range recipes {
		makes[r.Name] = r
	}
	var out []string
	for _, p := range db {
		r, ok := makes[p.Origin]
		if !ok {
			continue
		}
		if want := fmt.Sprintf("%s-r%d", r.Version, r.Epoch); p.Version != want {
			out = append(out, fmt.Sprintf("%s is %s in the root, not the recipe's %s: Wolfi's is above the recipe's; give recipes/%s.yaml a version or epoch above it",
				p.Name, p.Version, want, r.File))
		}
	}
	sort.Strings(out)
	return out
}

// checkRecipes (image check-recipes [rootfs.tar|initrd.tar]) fails when a root apko built holds
// Wolfi's package where a recipe's should be. It runs after apko in task rootfs and task initrd: a
// Wolfi bump above an override's version takes the override's place without a word otherwise,
// and shows up as whatever the override carried - a sysusers file, a unit, a build option.
func checkRecipes(rootTar string) error {
	recipes, err := readRecipes("recipes")
	if err != nil {
		return err
	}
	db, err := installedDB(rootTar)
	if err != nil {
		return err
	}
	if over := overtaken(recipes, db); len(over) > 0 {
		return fmt.Errorf("%s holds Wolfi's package where a recipe's should be:\n  %s", rootTar, strings.Join(over, "\n  "))
	}
	return nil
}
