// Package forks reads and moves what a recipe pins of a Construct-Linux fork: the repository,
// branch and commit its git-checkout names, and the package's version and epoch above them. The
// theme, the icons, the cursor and the extensions a recipe builds this way are upstream plus the
// fixes triaged against this stack, and a pin is how the image follows them.
package forks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Pin is one recipe's git-checkout of a Construct-Linux repository, with the package's version
// and epoch the recipe declares.
type Pin struct {
	Recipe  string // the recipe's path, as given
	Name    string // the repository under Construct-Linux
	Branch  string
	Commit  string // expected-commit, the fork's head the recipe builds
	Version string // the recipe's version line, without quotes
	Epoch   int
}

// checkout is a recipe's git-checkout of a Construct-Linux repository: melange's three lines,
// in the order the recipes write them.
var (
	checkout = regexp.MustCompile(`(?m)^[ \t]+repository: https://github\.com/Construct-Linux/([^\s/]+?)(?:\.git)?[ \t]*\n[ \t]+branch: (\S+)[ \t]*\n[ \t]+expected-commit: ([0-9a-f]{40})[ \t]*$`)
	version  = regexp.MustCompile(`(?m)^  version: *("?)([^"\s]+)"?[ \t]*$`)
	epoch    = regexp.MustCompile(`(?m)^  epoch: *([0-9]+)[ \t]*$`)
)

// Read is every fork the recipes in dir build from, in the recipes' order.
func Read(dir string) ([]Pin, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var pins []Pin
	for _, p := range paths {
		found, err := read(p)
		if err != nil {
			return nil, err
		}
		pins = append(pins, found...)
	}
	return pins, nil
}

func read(path string) ([]Pin, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(b)
	var pins []Pin
	for _, m := range checkout.FindAllStringSubmatch(s, -1) {
		p := Pin{Recipe: path, Name: m[1], Branch: m[2], Commit: m[3]}
		if v := version.FindStringSubmatch(s); v != nil {
			p.Version = v[2]
		}
		if e := epoch.FindStringSubmatch(s); e != nil {
			p.Epoch, _ = strconv.Atoi(e[1])
		}
		pins = append(pins, p)
	}
	return pins, nil
}

// Find is the fork the recipe name names (recipes/<name>.yaml), or the one whose repository is
// called name.
func Find(dir, name string) (Pin, error) {
	name = strings.TrimSuffix(name, ".yaml")
	pins, err := Read(dir)
	if err != nil {
		return Pin{}, err
	}
	for _, p := range pins {
		if strings.TrimSuffix(filepath.Base(p.Recipe), ".yaml") == name || p.Name == name {
			return p, nil
		}
	}
	return Pin{}, fmt.Errorf("no recipe in %s builds from Construct-Linux/%s", dir, name)
}

// Move rewrites p's recipe to pin commit at version and epoch, keeping the version line's quotes
// as they were. A recipe whose version or epoch did not move makes an apk the builder's cache
// already knows, so one of the two must change with the commit.
func (p Pin) Move(commit, ver string, ep int) error {
	if len(commit) != 40 {
		return fmt.Errorf("%q is no full commit hash", commit)
	}
	if ver == p.Version && ep == p.Epoch {
		return errors.New("the version or the epoch must move with the commit")
	}
	b, err := os.ReadFile(p.Recipe)
	if err != nil {
		return err
	}
	s := string(b)
	old := "expected-commit: " + p.Commit
	if strings.Count(s, old) != 1 {
		return fmt.Errorf("%s: expected-commit %s not found once", p.Recipe, p.Commit)
	}
	s = strings.Replace(s, old, "expected-commit: "+commit, 1)
	s = version.ReplaceAllString(s, "  version: ${1}"+ver+"${1}")
	s = epoch.ReplaceAllString(s, "  epoch: "+strconv.Itoa(ep))
	return os.WriteFile(p.Recipe, []byte(s), 0o644)
}
