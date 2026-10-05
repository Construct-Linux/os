package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A recipe's package, version and epoch come from its package block, and its needs from its
// runtime dependencies and build environment, constraints dropped.
func TestReadRecipe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gdm.yaml")
	body := `# A header comment.
package:
  name: gdm
  version: "51.0"
  epoch: 3
  dependencies:
    runtime:
      - gnome-shell
      - glib>=2.80

environment:
  contents:
    packages:
      - accountsservice-dev
      - build-base

pipeline:
  - uses: fetch
    with:
      uri: https://example
subpackages:
  - name: gdm-dev
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := readRecipe(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "gdm" || r.Version != "51.0" || r.Epoch != 3 || r.apk() != "gdm-51.0-r3.apk" ||
		strings.Join(r.Needs, ",") != "gnome-shell,glib,accountsservice-dev,build-base" {
		t.Errorf("%+v", r)
	}
}

// The recipes whose package is missing build in waves after the missing recipes they need, a subpackage
// counting as its recipe's.
func TestMissingInWaves(t *testing.T) {
	recipes := []recipe{
		{File: "gdm", Name: "gdm", Version: "51.0", Epoch: 3, Needs: []string{"accountsservice-dev", "gnome-shell"}},
		{File: "accountsservice", Name: "accountsservice", Version: "26", Epoch: 1},
		{File: "gnome-shell", Name: "gnome-shell", Version: "51.0", Epoch: 1, Needs: []string{"mutter-dev"}},
		{File: "mutter", Name: "mutter", Version: "51.0", Epoch: 0},
	}
	have := map[string]bool{"mutter-51.0-r0.apk": true}
	if got := fmt.Sprint(missingInWaves(recipes, have)); got != "[[accountsservice gnome-shell] [gdm]]" {
		t.Errorf("waves %s", got)
	}
	// mutter missing too: gnome-shell waits for it, gdm for gnome-shell.
	if got := fmt.Sprint(missingInWaves(recipes, nil)); got != "[[accountsservice mutter] [gnome-shell] [gdm]]" {
		t.Errorf("waves without mutter %s", got)
	}
	// A cycle builds, one side first.
	cycle := []recipe{{File: "a", Name: "a", Needs: []string{"b-dev"}}, {File: "b", Name: "b", Needs: []string{"a"}}}
	if got := fmt.Sprint(missingInWaves(cycle, nil)); got != "[[b] [a]]" {
		t.Errorf("a cycle %s", got)
	}
}

// Of a package's builds, every one but the newest is superseded, by version then epoch.
func TestSuperseded(t *testing.T) {
	got := superseded([]string{"APKINDEX.tar.gz", "gdm-51.0-r2.apk", "gdm-51.0-r3.apk", "gdm-dev-51.0-r3.apk",
		"orchis-theme-20260914-r9.apk", "orchis-theme-20260914-r12.apk", "mesa-26.2.10-r0.apk", "mesa-26.2.9-r4.apk"})
	if strings.Join(got, " ") != "gdm-51.0-r2.apk mesa-26.2.9-r4.apk orchis-theme-20260914-r9.apk" {
		t.Errorf("superseded %v", got)
	}
}

// A recipe changed since the branch left origin/main makes a new package only with a new version
// or epoch: the builder's cache (cache -pull, packages -missing) knows a package by its apk's
// name alone, so a changed recipe under the same name is a hit on the old build, and the release
// ships it. Without git or origin/main there is nothing to compare, and the test says so.
func TestChangedRecipesBumpVersionOrEpoch(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		return string(out), err
	}
	base, err := git("merge-base", "HEAD", "origin/main")
	if err != nil {
		t.Skipf("no merge-base with origin/main to compare recipes with: %v", err)
	}
	base = strings.TrimSpace(base)
	// The working tree against the base: what is committed on the branch and what is not yet.
	changed, err := git("diff", "--name-only", base, "--", "recipes")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range strings.Fields(changed) {
		name, _, _ := strings.Cut(strings.TrimPrefix(p, "recipes/"), "/")
		names[strings.TrimSuffix(name, ".yaml")] = true
	}
	for name := range names {
		file := filepath.Join(root, "recipes", name+".yaml")
		if _, err := os.Stat(file); err != nil {
			continue // removed
		}
		old, err := git("show", base+":recipes/"+name+".yaml")
		if err != nil {
			continue // new: no package of its name was built before
		}
		oldFile := filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(oldFile, []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		was, err := readRecipe(oldFile)
		if err != nil {
			t.Fatal(err)
		}
		now, err := readRecipe(file)
		if err != nil {
			t.Fatal(err)
		}
		if was.apk() == now.apk() {
			t.Errorf("recipes/%s changed since %.12s but still makes %s: raise its epoch", name, base, now.apk())
		}
	}
}

// apk's order: a release candidate before its release, a patch level and a snapshot after it, a
// shorter version older, a letter after none, numbers as numbers.
func TestAPKCompare(t *testing.T) {
	ordered := []string{"1.0_rc1", "1.0_rc2", "1.0", "1.0_p1", "1.0.1", "1.9", "1.10", "18.0_rc20260919", "18.0", "18.0_git20260101", "18.0_p2"}
	for i, older := range ordered {
		for _, newer := range ordered[i+1:] {
			if apkCompare(older, newer) != -1 || apkCompare(newer, older) != 1 {
				t.Errorf("%s is not older than %s", older, newer)
			}
		}
	}
	// A letter after none: 1.0 before 1.0a. Against a suffix apk orders by the tokens' kinds, which
	// no Wolfi version this repository builds has, and which this does not try.
	if apkCompare("1.0", "1.0a") != -1 {
		t.Error("1.0 is not older than 1.0a")
	}
	if apkCompare("1.9.17_p2", "1.9.17_p2") != 0 {
		t.Error("a version is not equal to itself")
	}
	// The epoch orders two builds of one version.
	if !versionLess("18.0", 1, "18.0", 2) || versionLess("18.0", 2, "18.0_rc1", 9) {
		t.Error("epochs")
	}
}
