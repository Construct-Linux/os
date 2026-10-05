package main

import (
	"bufio"
	"cmp"
	"flag"
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
// every version but its newest, by version and then epoch.
func superseded(files []string) []string {
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
		case versionLess(cur.version, cur.epoch, b.version, b.epoch):
			old = append(old, cur.file)
			newest[m[1]] = b
		default:
			old = append(old, b.file)
		}
	}
	sort.Strings(old)
	return old
}

// versionLess is whether build a (version and epoch) is older than b, as apk orders them.
func versionLess(a string, ea int, b string, eb int) bool {
	if c := apkCompare(a, b); c != 0 {
		return c < 0
	}
	return ea < eb
}

// apkSuffix orders apk's version suffixes against none, which is 0: a release candidate comes
// before the release, a patch level after it.
var apkSuffix = map[string]int{"alpha": -4, "beta": -3, "pre": -2, "rc": -1, "cvs": 1, "svn": 2, "git": 3, "hg": 4, "p": 5}

// apkVersion is a version apk's way: its numbers, an optional letter after the last, and its
// suffixes, each a rank and a number (18.0_rc20260919: [18 0], no letter, [rc 20260919]).
type apkVersion struct {
	nums     []int
	letter   byte
	suffixes [][2]int
}

func parseAPKVersion(v string) apkVersion {
	var p apkVersion
	main, rest, _ := strings.Cut(v, "_")
	if n := len(main); n > 0 && main[n-1] >= 'a' && main[n-1] <= 'z' {
		p.letter, main = main[n-1], main[:n-1]
	}
	for _, f := range strings.Split(main, ".") {
		n, _ := strconv.Atoi(f)
		p.nums = append(p.nums, n)
	}
	if rest == "" {
		return p
	}
	for _, suf := range strings.Split(rest, "_") {
		name := strings.TrimRight(suf, "0123456789")
		n, _ := strconv.Atoi(suf[len(name):])
		p.suffixes = append(p.suffixes, [2]int{apkSuffix[name], n})
	}
	return p
}

// apkCompare orders two versions as apk-tools does for what Wolfi's carry: numbers as numbers, a
// shorter one older when the rest is equal (1.0 before 1.0.1), then a letter after none, then the
// suffixes, where none sits between a release candidate and a patch level (18.0_rc1 before 18.0
// before 18.0_p1). A letter against a suffix apk orders by the tokens' kinds, which no version
// built here has.
func apkCompare(a, b string) int {
	pa, pb := parseAPKVersion(a), parseAPKVersion(b)
	for i := 0; i < len(pa.nums) && i < len(pb.nums); i++ {
		if c := cmp.Compare(pa.nums[i], pb.nums[i]); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(len(pa.nums), len(pb.nums)); c != 0 {
		return c
	}
	if c := cmp.Compare(pa.letter, pb.letter); c != 0 {
		return c
	}
	for i := 0; i < max(len(pa.suffixes), len(pb.suffixes)); i++ {
		var x, y [2]int
		if i < len(pa.suffixes) {
			x = pa.suffixes[i]
		}
		if i < len(pb.suffixes) {
			y = pb.suffixes[i]
		}
		if c := cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1])); c != 0 {
			return c
		}
	}
	return 0
}

// packagesCmd answers what packages/ lacks (-missing: recipe names, a wave a line) or takes out
// what newer builds replaced (-prune: the files it removed, for the index to be made again).
func packagesCmd(args []string) error {
	fl := flag.NewFlagSet("packages", flag.ExitOnError)
	missing := fl.Bool("missing", false, "print the recipes whose package packages/ lacks, a line per wave that builds side by side")
	prune := fl.Bool("prune", false, "remove every package's builds but its newest")
	// The packages.
	dir := filepath.Join("packages", "x86_64")
	_ = fl.Parse(args)
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
	switch {
	case *missing:
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
	case *prune:
		for _, f := range superseded(files) {
			if err := os.Remove(filepath.Join(dir, f)); err != nil {
				return err
			}
			fmt.Println(f)
		}
	default:
		return fmt.Errorf("packages: -missing or -prune")
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
