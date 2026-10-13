package main

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// packages/ between builds. The release builder starts from the cache (infra's packages bucket)
// and builds only the recipes whose package the cache lacks, in the order their build environments
// need each other; before the cache goes back, every package but its newest build goes, so the
// cache holds what the recipes make and no history.

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

var apkName = regexp.MustCompile(`^(.+)-([^-]+)-r([0-9]+)\.apk$`)

// superseded are the APKs in files that a newer build of the same package replaces: a package's
// every version but its newest, by version and then epoch. A version it cannot order stops it:
// what it answers is deleted.
func superseded(files []string) ([]string, error) {
	type build struct {
		file, version string
		epoch         int
	}
	newest := map[string]build{}
	var old []string
	for _, f := range files {
		m := apkName.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		e, _ := strconv.Atoi(m[3])
		b := build{f, m[2], e}
		cur, ok := newest[m[1]]
		switch {
		case !ok:
			newest[m[1]] = b
		default:
			less, err := versionLess(cur.version, cur.epoch, b.version, b.epoch)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if less {
				old = append(old, cur.file)
				newest[m[1]] = b
			} else {
				old = append(old, b.file)
			}
		}
	}
	sort.Strings(old)
	return old, nil
}

// versionLess is whether build a (version and epoch) is older than b, as apko orders them.
func versionLess(a string, ea int, b string, eb int) (bool, error) {
	c, err := apkCompare(a, b)
	if c != 0 || err != nil {
		return c < 0, err
	}
	return ea < eb, nil
}

// apkVersionRE is the versions apko takes (chainguard.dev/apko's ParseVersion) - and apko picks
// the build of each package the image gets, so prune keeps the one apko would: numbers by dots, a
// letter, at most one pre-release suffix and then at most one post-release one, each with an
// optional number. A version outside it is refused rather than ordered by a guess.
var apkVersionRE = regexp.MustCompile(`^([0-9]+)((?:\.[0-9]+)*)([a-z]?)(?:_(alpha|beta|pre|rc)([0-9]*))?(?:_(cvs|svn|git|hg|p)([0-9]*))?$`)

// apkPre and apkPost rank the suffixes: a pre-release before none, which is last of them (1.0_rc1
// before 1.0), and none before a post-release (1.0 before 1.0_p1).
var (
	apkPre  = map[string]int{"alpha": 1, "beta": 2, "pre": 3, "rc": 4, "": 5}
	apkPost = map[string]int{"": 0, "cvs": 1, "svn": 2, "git": 3, "hg": 4, "p": 5}
)

// apkVersion is a version as apko orders it: its numbers as numbers, then its letter, its
// pre-release suffix and number, and its post-release suffix and number.
type apkVersion struct {
	nums                       []int
	letter                     byte
	pre, preNum, post, postNum int
}

func parseAPKVersion(v string) (apkVersion, error) {
	m := apkVersionRE.FindStringSubmatch(v)
	if m == nil {
		return apkVersion{}, fmt.Errorf("%q is no version apko orders", v)
	}
	var p apkVersion
	for _, f := range strings.Split(m[1]+m[2], ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return apkVersion{}, fmt.Errorf("%q: %w", v, err)
		}
		p.nums = append(p.nums, n)
	}
	if m[3] != "" {
		p.letter = m[3][0]
	}
	p.pre, p.post = apkPre[m[4]], apkPost[m[6]]
	for _, n := range []struct {
		s  string
		to *int
	}{{m[5], &p.preNum}, {m[7], &p.postNum}} {
		if n.s == "" {
			continue
		}
		var err error
		if *n.to, err = strconv.Atoi(n.s); err != nil {
			return apkVersion{}, fmt.Errorf("%q: %w", v, err)
		}
	}
	return p, nil
}

// apkCompare orders a and b as apko does, or says why it cannot. TestAPKCompareMatchesApko holds it
// to apko itself (tools/apkorder).
func apkCompare(a, b string) (int, error) {
	pa, err := parseAPKVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseAPKVersion(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(pa.nums) && i < len(pb.nums); i++ {
		if c := cmp.Compare(pa.nums[i], pb.nums[i]); c != 0 {
			return c, nil
		}
	}
	return cmp.Or(
		cmp.Compare(len(pa.nums), len(pb.nums)),
		cmp.Compare(pa.letter, pb.letter),
		cmp.Compare(pa.pre, pb.pre), cmp.Compare(pa.preNum, pb.preNum),
		cmp.Compare(pa.post, pb.post), cmp.Compare(pa.postNum, pb.postNum),
	), nil
}

// packagesCmd is image packages missing, which prints the recipes whose package packages/ lacks,
// a line per wave that builds side by side, or image packages prune, which removes every
// package's builds but its newest and prints the files it removed, for the index to be made
// again.
func packagesCmd(args []string) error {
	if len(args) != 1 || (args[0] != "missing" && args[0] != "prune") {
		return errors.New("packages missing|prune")
	}
	dir := filepath.Join("packages", "x86_64")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var files []string
	have := map[string]bool{}
	for _, e := range entries {
		files = append(files, e.Name())
		have[e.Name()] = true
	}
	if args[0] == "missing" {
		paths, err := filepath.Glob(filepath.Join("recipes", "*.yaml"))
		if err != nil {
			return err
		}
		var recipes []recipe
		for _, p := range paths {
			r, err := readRecipe(p)
			if err != nil {
				return err
			}
			r.File = strings.TrimSuffix(filepath.Base(p), ".yaml")
			recipes = append(recipes, r)
		}
		for _, w := range missingInWaves(recipes, have) {
			fmt.Println(strings.Join(w, " "))
		}
		return nil
	}
	old, err := superseded(files)
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

// apkPackage is one package of an apk database or index: its name, its version, and the files
// it installed, relative to the root (an index lists none).
type apkPackage struct {
	Name, Version string
	Files         []string
}

// readAPKDB reads apk's database format, blocks of one-letter fields: P: names a package, V: is
// its version, F: a directory and the R: lines after it the files in it.
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
		case strings.HasPrefix(line, "F:"):
			dir = line[2:]
		case strings.HasPrefix(line, "R:"):
			p.Files = append(p.Files, filepath.Join(dir, line[2:]))
		}
	}
	return pkgs, sc.Err()
}
