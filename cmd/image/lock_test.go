package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A lock written is the lock read back: the configuration it includes and its two parts, each
// sorted by name.
func TestLockFileRoundTrip(t *testing.T) {
	root := lockedRoot{Name: "rootfs", Config: "image/apko.yaml", Lock: filepath.Join(t.TempDir(), "apko.lock.yaml"), Task: "rootfs"}
	l := lockFile{Config: root.Config, Ours: []pin{{"pcsc-lite", "2.5.3-r1"}, {"mesa", "26.2.5-r1"}}, Wolfi: []pin{{"zlib", "1.3.2.1_rc20260921-r0"}, {"glib", "2.86.0-r0"}}, Provided: []string{"glibc-locale-es", "glibc-locale-en"}}
	if err := l.write(root); err != nil {
		t.Fatal(err)
	}
	got, err := readLockFile(root.Lock)
	if err != nil {
		t.Fatal(err)
	}
	want := lockFile{Config: root.Config, Ours: []pin{{"mesa", "26.2.5-r1"}, {"pcsc-lite", "2.5.3-r1"}}, Wolfi: []pin{{"glib", "2.86.0-r0"}, {"zlib", "1.3.2.1_rc20260921-r0"}}}
	if strings.Join(changes(want, got), "\n") != "" || got.Config != want.Config || len(got.Ours) != 2 || len(got.Wolfi) != 2 || strings.Join(got.Provided, " ") != "glibc-locale-en glibc-locale-es" {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if un := got.uncovered([]string{"mesa", "glibc-locale-en", "new"}); strings.Join(un, " ") != "glibc-locale-en new" {
		t.Errorf("uncovered: %v", un)
	}
	if none, err := readLockFile(filepath.Join(t.TempDir(), "none")); err != nil || len(none.pinned()) != 0 {
		t.Errorf("a lock not written yet is empty: %+v, %v", none, err)
	}
}

// The lock apko's resolution makes pins the recipes' packages as resolved from packages/ and
// Wolfi's at the old lock's versions, or as resolved with all; what changed is said. A recipe
// whose package packages/ lacks, and one whose package resolved to Wolfi's, fail it.
func TestLockResolution(t *testing.T) {
	recipes := []recipe{{File: "pcsc-lite", Name: "pcsc-lite", Version: "2.5.3", Epoch: 1}}
	ours := []apkPackage{{Name: "pcsc-lite", Version: "2.5.3-r1", Origin: "pcsc-lite"}, {Name: "pcsc-lite-doc", Version: "2.5.3-r1", Origin: "pcsc-lite"}}
	resolved := []resolvedPackage{
		{"pcsc-lite", "/work/packages/x86_64/pcsc-lite-2.5.3-r1.apk", "2.5.3-r1"},
		{"zlib", "https://packages.wolfi.dev/os/x86_64/zlib-1.3.2.1_rc20260921-r0.apk", "1.3.2.1_rc20260921-r0"},
		{"glib", "https://packages.wolfi.dev/os/x86_64/glib-2.86.0-r0.apk", "2.86.0-r0"},
	}
	old := lockFile{Config: "image/apko.yaml", Ours: []pin{{"pcsc-lite", "2.5.2-r1"}}, Wolfi: []pin{{"zlib", "1.3.2.1_rc20260917-r0"}, {"gone", "1-r0"}}}
	l, err := lockResolution(resolved, old, false, recipes, ours)
	if err != nil {
		t.Fatal(err)
	}
	want := "+ glib=2.86.0-r0\n- gone=1-r0\n  pcsc-lite 2.5.2-r1 -> 2.5.3-r1"
	if got := strings.Join(changes(old, l), "\n"); got != want {
		t.Errorf("kept Wolfi's:\n%s\nwant:\n%s", got, want)
	}
	l, err = lockResolution(resolved, old, true, recipes, ours)
	if err != nil {
		t.Fatal(err)
	}
	want = "+ glib=2.86.0-r0\n- gone=1-r0\n  pcsc-lite 2.5.2-r1 -> 2.5.3-r1\n  zlib 1.3.2.1_rc20260917-r0 -> 1.3.2.1_rc20260921-r0"
	if got := strings.Join(changes(old, l), "\n"); got != want {
		t.Errorf("all:\n%s\nwant:\n%s", got, want)
	}
	if _, err := lockResolution(resolved, old, false, recipes, nil); err == nil || !strings.Contains(err.Error(), "task build -- pcsc-lite") {
		t.Errorf("packages/ without the recipe's package: %v", err)
	}
	wolfis := append([]resolvedPackage{{"pcsc-lite-doc", "https://packages.wolfi.dev/os/x86_64/pcsc-lite-doc-2.5.4-r0.apk", "2.5.4-r0"}}, resolved...)
	if _, err := lockResolution(wolfis, old, false, recipes, ours); err == nil || !strings.Contains(err.Error(), "pcsc-lite-doc resolved to Wolfi's 2.5.4-r0, above the recipe's 2.5.3-r1: give recipes/pcsc-lite.yaml") {
		t.Errorf("a recipe's subpackage resolved to Wolfi's: %v", err)
	}
}

// Wolfi's lines are bumped to the newest version the index has, as apk orders versions; the
// recipes' lines and a line the index no longer has stay.
func TestBumped(t *testing.T) {
	l := lockFile{Ours: []pin{{"pcsc-lite", "2.5.3-r1"}}, Wolfi: []pin{{"zlib", "1.3.2.1_rc20260917-r0"}, {"mesa-libs", "18.0_rc20260919-r0"}, {"gone", "1-r0"}}}
	index := []apkPackage{
		{Name: "zlib", Version: "1.3.2.1_rc20260921-r0"}, {Name: "zlib", Version: "1.3.2.1_rc20260917-r0"},
		{Name: "mesa-libs", Version: "18.0-r0"}, {Name: "mesa-libs", Version: "18.0_rc20260919-r0"},
		{Name: "pcsc-lite", Version: "2.5.4-r0"},
	}
	want := "  mesa-libs 18.0_rc20260919-r0 -> 18.0-r0\n  zlib 1.3.2.1_rc20260917-r0 -> 1.3.2.1_rc20260921-r0"
	if got := strings.Join(changes(l, bumped(l, index)), "\n"); got != want {
		t.Errorf("bumped:\n%s\nwant:\n%s", got, want)
	}
}

// What the root installs beyond the lock's pins is unlocked, at another version too.
func TestUnlocked(t *testing.T) {
	l := lockFile{Ours: []pin{{"pcsc-lite", "2.5.3-r1"}}, Wolfi: []pin{{"zlib", "1-r0"}}}
	db := []apkPackage{{Name: "pcsc-lite", Version: "2.5.3-r1"}, {Name: "zlib", Version: "2-r0"}, {Name: "new", Version: "1-r0"}}
	if got := strings.Join(unlocked(l, db), " "); got != "new=1-r0 zlib=2-r0" {
		t.Errorf("unlocked: %s", got)
	}
}

// The locks in the repository pin every package the configurations name, or know it as
// provided: a package added without task lock would come in at whatever version Wolfi has that
// hour. CI's check, which builds no root.
func TestLocksCoverConfigs(t *testing.T) {
	t.Chdir("../..")
	for _, root := range lockedRoots {
		l, err := readLockFile(root.Lock)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.pinned()) == 0 {
			t.Fatalf("%s pins nothing: task lock", root.Lock)
		}
		names, err := configPackages(root.Config)
		if err != nil {
			t.Fatal(err)
		}
		provided := map[string]bool{}
		for _, p := range l.Provided {
			provided[p] = true
		}
		for _, n := range l.uncovered(names) {
			if !provided[n] {
				t.Errorf("%s names %s, which %s does not pin: task lock", root.Config, n, root.Lock)
			}
		}
	}
}
