package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// Prune keeps what the recipes make: a package's build at its recipe's version and epoch, and no
// other - an older one, a newer one a recipe took back, or one of a recipe that is gone.
func TestUnmade(t *testing.T) {
	dir := t.TempDir()
	for _, a := range []struct{ name, origin, pkgver string }{
		{"gdm-51.0-r2", "gdm", "51.0-r2"}, {"gdm-51.0-r3", "gdm", "51.0-r3"}, {"gdm-dev-51.0-r3", "gdm", "51.0-r3"},
		{"udev-261.3-r6", "systemd", "261.3-r6"}, {"udev-261.3-r7", "systemd", "261.3-r7"},
		{"mesa-26.2.9-r4", "mesa", "26.2.9-r4"},
	} {
		apkOf(t, filepath.Join(dir, a.name+".apk"), "origin = "+a.origin+"\npkgver = "+a.pkgver+"\n")
	}
	must(t, os.WriteFile(filepath.Join(dir, "APKINDEX.tar.gz"), nil, 0o644))
	recipes := []recipe{{Name: "gdm", Version: "51.0", Epoch: 3}, {Name: "systemd", Version: "261.3", Epoch: 6}}
	got, err := unmade(dir, recipes)
	if err != nil || strings.Join(got, " ") != "gdm-51.0-r2.apk mesa-26.2.9-r4.apk udev-261.3-r7.apk" {
		t.Errorf("unmade %v %v", got, err)
	}
	// An apk that does not say what made it stops prune before it names a file to delete.
	apkOf(t, filepath.Join(dir, "stray-1.0-r0.apk"), "pkgname = stray\n")
	if got, err := unmade(dir, recipes); err == nil {
		t.Errorf("a stray apk was ordered: %v", got)
	}
}

// apkOf writes an apk at path with pkginfo as its .PKGINFO: as melange lays it, a tar in gzip,
// the control segment, with a file of the package after it.
func apkOf(t *testing.T, path, pkginfo string) {
	t.Helper()
	f, err := os.Create(path)
	must(t, err)
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range []struct{ name, body string }{{".PKGINFO", pkginfo}, {"usr/bin/x", "#!/bin/sh\n"}} {
		must(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))}))
		_, err = tw.Write([]byte(e.body))
		must(t, err)
	}
	must(t, tw.Close())
	must(t, gz.Close())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A recipe changed since the branch left origin/main makes a new package only with a new version
// or epoch: the builder's cache (cache pull, packages missing) knows a package by its apk's
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
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if was.apk() == now.apk() && withoutComments(old) != withoutComments(string(text)) {
			t.Errorf("recipes/%s changed since %.12s but still makes %s: raise its epoch", name, base, now.apk())
		}
	}
}

// withoutComments is a recipe without its blank lines and the lines that are only a comment, so
// a recipe whose comments alone changed builds what it built. A # inside a block scalar (a
// pipeline's runs: |) is the block's own text, which may land in a package, and stays.
func withoutComments(recipe string) string {
	var out []string
	block := -1 // the indent of the line that opened a block scalar, or -1 outside one
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if block >= 0 && (trimmed == "" || indent > block) {
			out = append(out, line)
			continue
		}
		block = -1
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if blockScalar.MatchString(trimmed) {
			block = indent
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

var blockScalar = regexp.MustCompile(`(^-|:)\s*[|>][-+0-9]*$`)

func TestWithoutComments(t *testing.T) {
	was := "# old header\npackage:\n  name: x\n  # old\npipeline:\n  - runs: |\n      # kept\n      make\n"
	if got := withoutComments("# new header\n\npackage:\n  name: x\npipeline:\n  - runs: |\n      # kept\n      make\n"); got != withoutComments(was) {
		t.Errorf("only the comments outside block scalars changed, yet %q differs from %q", got, withoutComments(was))
	}
	if withoutComments(strings.Replace(was, "# kept", "# changed", 1)) == withoutComments(was) {
		t.Error("a # line inside a block scalar is the block's text, and a change to it is a change")
	}
}
