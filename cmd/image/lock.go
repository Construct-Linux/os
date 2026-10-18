package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/apkver"
)

// A build installs the versions its lock says, not the newest Wolfi has that hour: two builds of
// one commit are the same root, a release's packages are the commit's, and a Wolfi bump reaches a
// build only when a commit takes it. The lock is an apko configuration apko builds instead of the
// hand-written one, which it includes: apko.yaml names what the root is for, the lock pins every
// package that follows, name=version. apko's own lock file (apko lock, apko build --lockfile) is
// not used: it pins a package to its apk's checksum, and the checksum of a package recipes/ builds
// changes with the commit it is built at (melange writes the commit into .PKGINFO), so a lock
// made on one machine refuses the builder's own build of the same recipe.
//
// The pins come from apko's resolution (task lock: apko lock writes it as JSON, image lock write
// reads it): the recipes' packages at their build in packages/, and Wolfi's at the lock's
// versions, or at Wolfi's newest with -all. Between locks, the lock workflow bumps Wolfi's lines
// to its index every Monday, in a pull request (image lock index), without resolving: resolving
// needs packages/, which no runner has.

// lockFile is image/apko.lock.yaml or image/initrd/apko.lock.yaml: the configuration it
// includes, its pins in two parts, the packages recipes/ builds and Wolfi's, and the names the
// configuration asks for that no pin carries, because a package pinned under another name
// provides them (glibc-locale-en is glibc-2.43-locale-en's): the test that holds a configuration
// to its lock knows them by this.
type lockFile struct {
	Config      string
	Ours, Wolfi []pin
	Provided    []string
}

// pin is a package at one version, as apko takes it: name=version.
type pin struct{ Name, Version string }

// lockedRoot is a root that is built from a lock: the configuration a person writes, the lock
// apko builds, and the task that builds it.
type lockedRoot struct{ Name, Config, Lock, Task string }

// lockedRoots are the roots that are locked, by the tar task rootfs or task initrd makes.
var lockedRoots = map[string]lockedRoot{
	"rootfs.tar": {"rootfs", "image/apko.yaml", "image/apko.lock.yaml", "rootfs"},
	"initrd.tar": {"initrd", "image/initrd/apko.yaml", "image/initrd/apko.lock.yaml", "initrd"},
}

// The marks that open the lock's two parts; image lock index reads them to bump Wolfi's alone.
const (
	oursMark     = "    # recipes/' packages, at their build in packages/."
	wolfiMark    = "    # Wolfi's: bumped by the lock workflow, or task lock -- -all."
	providedMark = "    # Named by the configuration, provided by a package above under another name:"
)

// readLockFile reads a lock; one that does not exist yet is empty.
func readLockFile(path string) (lockFile, error) {
	var l lockFile
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	var part *[]pin
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "include: "):
			l.Config = strings.TrimPrefix(line, "include: ")
		case line == oursMark:
			part = &l.Ours
		case line == wolfiMark:
			part = &l.Wolfi
		case line == providedMark:
			part = nil
		case strings.HasPrefix(line, "    #   "):
			l.Provided = strings.Fields(line)[1:]
		case strings.HasPrefix(line, "    - "):
			name, version, ok := strings.Cut(strings.TrimPrefix(line, "    - "), "=")
			if !ok || part == nil {
				return l, fmt.Errorf("%s: %q is not name=version under a part of the lock", path, line)
			}
			*part = append(*part, pin{name, version})
		}
	}
	return l, nil
}

// write writes the lock for root, its parts sorted by name.
func (l lockFile) write(root lockedRoot) error {
	var b strings.Builder
	fmt.Fprintf(&b, `# Every package this root installs, pinned to the version it installs. Written by task lock;
# built by apko in task %s, in place of the configuration it includes, where a package comes
# and goes - task lock pins what follows. Never edited by hand. A Wolfi bump reaches a build only
# here: the lock workflow bumps Wolfi's lines to its index every Monday, in a pull request; task
# lock resolves the set again with apko, takes a bumped recipe's package and keeps Wolfi's lines,
# or with -- -all takes Wolfi's newest too.
include: %s
contents:
  packages:
`, root.Task, root.Config)
	for _, part := range []struct {
		mark string
		pins []pin
	}{{oursMark, l.Ours}, {wolfiMark, l.Wolfi}} {
		sort.Slice(part.pins, func(i, j int) bool { return part.pins[i].Name < part.pins[j].Name })
		b.WriteString(part.mark + "\n")
		for _, p := range part.pins {
			fmt.Fprintf(&b, "    - %s=%s\n", p.Name, p.Version)
		}
	}
	if len(l.Provided) > 0 {
		sort.Strings(l.Provided)
		fmt.Fprintf(&b, "%s\n    #   %s\n", providedMark, strings.Join(l.Provided, " "))
	}
	return os.WriteFile(root.Lock, []byte(b.String()), 0o644)
}

// configPackages are the packages a configuration names, in its order.
func configPackages(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case line == "  packages:":
			in = true
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    "):
			in = false
		case in && strings.HasPrefix(line, "    - "):
			names = append(names, strings.TrimPrefix(line, "    - "))
		}
	}
	return names, nil
}

// uncovered are the names of a configuration that the lock pins under no name: what a pinned
// package provides, or what came in without task lock.
func (l lockFile) uncovered(names []string) []string {
	pinned := l.pinned()
	var out []string
	for _, n := range names {
		if pinned[n] == "" {
			out = append(out, n)
		}
	}
	return out
}

// pinned is every pin of the lock, by name.
func (l lockFile) pinned() map[string]string {
	m := make(map[string]string, len(l.Ours)+len(l.Wolfi))
	for _, p := range l.Ours {
		m[p.Name] = p.Version
	}
	for _, p := range l.Wolfi {
		m[p.Name] = p.Version
	}
	return m
}

// resolvedPackage is one package of apko lock's JSON: where it was resolved from says whose it
// is, packages/ (a path) or Wolfi (a URL).
type resolvedPackage struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

func (p resolvedPackage) ours() bool { return !strings.Contains(p.URL, "://") }

// readResolution reads the packages apko lock resolved.
func readResolution(path string) ([]resolvedPackage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l struct {
		Contents struct {
			Packages []resolvedPackage `json:"packages"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(l.Contents.Packages) == 0 {
		return nil, fmt.Errorf("%s resolves no package", path)
	}
	return l.Contents.Packages, nil
}

// lockResolution is the lock apko's resolution makes: the recipes' packages as resolved, Wolfi's
// at old's versions where old pins them, or as resolved with all. It fails when a recipe's
// package is missing from packages/ (apko resolved Wolfi's, or nothing, in its place) or when
// Wolfi's version of one has climbed above the recipe's: an override's whole point is what
// Wolfi's lacks. ours is packages/' index.
func lockResolution(resolved []resolvedPackage, old lockFile, all bool, recipes []recipe, ours []apkPackage) (lockFile, error) {
	made := map[string]bool{}
	origin := map[string]string{}
	for _, p := range ours {
		made[p.Name+"="+p.Version] = true
		origin[p.Name] = p.Origin
	}
	byRecipe := map[string]recipe{}
	for _, r := range recipes {
		byRecipe[r.Name] = r
	}
	var fails []string
	for _, r := range recipes {
		if !made[r.Name+"="+fmt.Sprintf("%s-r%d", r.Version, r.Epoch)] {
			fails = append(fails, fmt.Sprintf("packages/ lacks what recipes/%s.yaml makes: task build -- %s", r.File, r.File))
		}
	}
	l := lockFile{Config: old.Config}
	kept := old.pinned()
	for _, p := range resolved {
		switch {
		case p.ours():
			l.Ours = append(l.Ours, pin{p.Name, p.Version})
		case origin[p.Name] != "" && byRecipe[origin[p.Name]].Name != "":
			r := byRecipe[origin[p.Name]]
			fails = append(fails, fmt.Sprintf("%s resolved to Wolfi's %s, above the recipe's %s-r%d: give recipes/%s.yaml a version or epoch above it",
				p.Name, p.Version, r.Version, r.Epoch, r.File))
		case !all && kept[p.Name] != "":
			l.Wolfi = append(l.Wolfi, pin{p.Name, kept[p.Name]})
		default:
			l.Wolfi = append(l.Wolfi, pin{p.Name, p.Version})
		}
	}
	if len(fails) > 0 {
		sort.Strings(fails)
		return lockFile{}, errors.New(strings.Join(fails, "\n"))
	}
	return l, nil
}

// changes says what differs from old to new, a line per package, sorted: bumps, then what came
// and went.
func changes(old, new lockFile) []string {
	before, after := old.pinned(), new.pinned()
	var out []string
	for name, v := range after {
		switch was := before[name]; {
		case was == "":
			out = append(out, fmt.Sprintf("+ %s=%s", name, v))
		case was != v:
			out = append(out, fmt.Sprintf("  %s %s -> %s", name, was, v))
		}
	}
	for name, was := range before {
		if after[name] == "" {
			out = append(out, fmt.Sprintf("- %s=%s", name, was))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][2:] < out[j][2:] })
	return out
}

// bumped is the lock with Wolfi's lines at the newest version index has of each; a package the
// index no longer has keeps its line.
func bumped(l lockFile, index []apkPackage) lockFile {
	newest := map[string]string{}
	for _, p := range index {
		if cur, ok := newest[p.Name]; ok && apkver.Compare(p.Version, cur) <= 0 {
			continue
		}
		newest[p.Name] = p.Version
	}
	out := lockFile{Config: l.Config, Ours: l.Ours, Provided: l.Provided}
	for _, p := range l.Wolfi {
		if v := newest[p.Name]; v != "" {
			p.Version = v
		}
		out.Wolfi = append(out.Wolfi, p)
	}
	return out
}

// unlocked are the packages of db the lock does not pin at their version: what came in without
// task lock, a package added to the configuration or a dependency Wolfi's bump brought.
func unlocked(l lockFile, db []apkPackage) []string {
	pinned := l.pinned()
	var out []string
	for _, p := range db {
		if pinned[p.Name] != p.Version {
			out = append(out, p.Name+"="+p.Version)
		}
	}
	sort.Strings(out)
	return out
}

// readIndex reads the packages of an APKINDEX.tar.gz, a path or an https URL.
func readIndex(src string) ([]apkPackage, error) {
	var db []apkPackage
	err := withIndex(src, func(r io.Reader) error {
		var err error
		db, err = readAPKDB(r)
		return err
	})
	return db, err
}

// lockCmd is image lock write [-all], which writes the locks from what apko lock resolved into
// _output/lock and says what changed, or image lock index, which bumps Wolfi's lines to its
// index and says what changed, as Markdown for the pull request.
func lockCmd(args []string) error {
	if len(args) == 0 || (args[0] != "write" && args[0] != "index") {
		return errors.New("lock write [-all] | lock index")
	}
	roots := []lockedRoot{lockedRoots["rootfs.tar"], lockedRoots["initrd.tar"]}
	if args[0] == "index" {
		index, err := readIndex(wolfiIndex)
		if err != nil {
			return fmt.Errorf("%s: %w", wolfiIndex, err)
		}
		fmt.Printf("Wolfi's packages, at its index on %s.\n", time.Now().UTC().Format("2006-01-02"))
		for _, root := range roots {
			old, err := readLockFile(root.Lock)
			if err != nil {
				return err
			}
			l := bumped(old, index)
			if err := l.write(root); err != nil {
				return err
			}
			if ch := changes(old, l); len(ch) > 0 {
				fmt.Printf("\n### %s\n\n| package | from | to |\n|---|---|---|\n", root.Lock)
				for _, c := range ch {
					f := strings.Fields(c)
					fmt.Printf("| %s | %s | %s |\n", f[0], f[1], f[3])
				}
			}
		}
		return nil
	}
	all := len(args) > 1 && args[1] == "-all"
	recipes, err := readRecipes("recipes")
	if err != nil {
		return err
	}
	ours, err := readIndex(filepath.Join("packages", "x86_64", "APKINDEX.tar.gz"))
	if err != nil {
		return fmt.Errorf("packages/ has no index: task build -- <recipe> makes it: %w", err)
	}
	for _, root := range roots {
		resolved, err := readResolution(filepath.Join(out, "lock", root.Name+".lock.json"))
		if err != nil {
			return err
		}
		old, err := readLockFile(root.Lock)
		if err != nil {
			return err
		}
		old.Config = root.Config
		l, err := lockResolution(resolved, old, all, recipes, ours)
		if err != nil {
			return err
		}
		names, err := configPackages(root.Config)
		if err != nil {
			return err
		}
		l.Provided = l.uncovered(names)
		if err := l.write(root); err != nil {
			return err
		}
		ch := changes(old, l)
		fmt.Printf("%s: %d packages, %d changed\n", root.Lock, len(l.Ours)+len(l.Wolfi), len(ch))
		for _, c := range ch {
			fmt.Println(c)
		}
	}
	return nil
}

// checkLock (image check-lock rootfs.tar|initrd.tar) fails when a root apko built holds a
// package its lock does not pin, or Wolfi's package where a recipe's should be. It runs after
// apko in task rootfs and task initrd: a package added to the configuration, or brought by a
// Wolfi bump, comes in unpinned until task lock; and a Wolfi bump above an override's version
// takes the override's place without a word otherwise, and shows up as whatever the override
// carried - a sysusers file, a unit, a build option.
func checkLock(tar string) error {
	root, ok := lockedRoots[tar]
	if !ok {
		return errors.New("check-lock rootfs.tar|initrd.tar")
	}
	recipes, err := readRecipes("recipes")
	if err != nil {
		return err
	}
	l, err := readLockFile(root.Lock)
	if err != nil {
		return err
	}
	db, err := installedDB(filepath.Join(out, tar))
	if err != nil {
		return err
	}
	var fails []string
	if un := unlocked(l, db); len(un) > 0 {
		fails = append(fails, fmt.Sprintf("%s installs what %s does not pin, so a build may differ; task lock:\n  %s", tar, root.Lock, strings.Join(un, "\n  ")))
	}
	if over := overtaken(recipes, db); len(over) > 0 {
		fails = append(fails, fmt.Sprintf("%s holds Wolfi's package where a recipe's should be:\n  %s", tar, strings.Join(over, "\n  ")))
	}
	if len(fails) > 0 {
		return errors.New(strings.Join(fails, "\n"))
	}
	return nil
}
