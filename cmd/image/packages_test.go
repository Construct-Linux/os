package main

import (
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

// The recipes whose package is missing build after the missing recipes they need, a subpackage
// counting as its recipe's.
func TestMissingInOrder(t *testing.T) {
	recipes := []recipe{
		{File: "gdm", Name: "gdm", Version: "51.0", Epoch: 3, Needs: []string{"accountsservice-dev", "gnome-shell"}},
		{File: "accountsservice", Name: "accountsservice", Version: "26", Epoch: 1},
		{File: "gnome-shell", Name: "gnome-shell", Version: "51.0", Epoch: 1, Needs: []string{"mutter-dev"}},
		{File: "mutter", Name: "mutter", Version: "51.0", Epoch: 0},
	}
	have := map[string]bool{"mutter-51.0-r0.apk": true}
	if got := strings.Join(missingInOrder(recipes, have), " "); got != "accountsservice gnome-shell gdm" {
		t.Errorf("order %s", got)
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
