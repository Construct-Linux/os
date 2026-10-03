package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSyncThemeFollowsTheImage(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "gtk-4.0")
	writeTree(t, src, map[string]string{"gtk.css": "v1", "assets/a.svg": "a", "assets/old.svg": "old"})
	if ok, err := syncTheme(src, dst); err != nil || !ok {
		t.Fatalf("first sync: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "v1" {
		t.Fatalf("gtk.css = %q", got)
	}
	// The image updates: new contents, one asset gone.
	if err := os.Remove(filepath.Join(src, "assets/old.svg")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, map[string]string{"gtk.css": "v2"})
	if ok, err := syncTheme(src, dst); err != nil || !ok {
		t.Fatalf("second sync: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "v2" {
		t.Fatalf("gtk.css after update = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "assets/old.svg")); !os.IsNotExist(err) {
		t.Fatalf("an asset the image dropped is still there: %v", err)
	}
}

func TestSyncThemeLeavesTheUsersTheme(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "gtk-4.0")
	writeTree(t, src, map[string]string{"gtk.css": "v1"})
	if _, err := syncTheme(src, dst); err != nil {
		t.Fatal(err)
	}
	// The user puts their own theme there.
	writeTree(t, dst, map[string]string{"gtk.css": "mine"})
	writeTree(t, src, map[string]string{"gtk.css": "v2"})
	if ok, err := syncTheme(src, dst); err != nil || ok {
		t.Fatalf("sync over the user's theme: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "mine" {
		t.Fatalf("the user's gtk.css was replaced: %q", got)
	}
}

func TestSyncThemeLeavesALinkedTheme(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"gtk.css": "v1"})
	other := filepath.Join(t.TempDir(), "gtk.css")
	writeTree(t, filepath.Dir(other), map[string]string{"gtk.css": "linked"})
	// What Orchis's own install.sh -l leaves: a link, and no stamp.
	if err := os.Symlink(other, filepath.Join(dst, "gtk.css")); err != nil {
		t.Fatal(err)
	}
	if ok, err := syncTheme(src, dst); err != nil || ok {
		t.Fatalf("sync over a linked theme: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "linked" {
		t.Fatalf("the linked gtk.css changed: %q", got)
	}
}

// The GTK 4 theme goes where libadwaita reads, each GTK 3 variant where GTK 3 finds a theme from
// inside a Flatpak, and nothing else of the theme (the shell's) is copied.
func TestThemeCopiesGTK3AndGTK4(t *testing.T) {
	themes, home := t.TempDir(), t.TempDir()
	config := filepath.Join(home, ".config")
	for _, name := range []string{"Orchis-Construct-Dark", "Orchis-Construct-Light"} {
		writeTree(t, filepath.Join(themes, name), map[string]string{
			"gtk-3.0/gtk.css": name + " 3", "gtk-4.0/gtk.css": name + " 4", "gnome-shell/gnome-shell.css": "shell",
		})
	}
	var out strings.Builder
	if err := themeCmd([]string{"--themes", themes, "--home", home, "--config", config}, &out); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(config, "gtk-4.0/gtk.css")); got != "Orchis-Construct-Light 4" {
		t.Errorf("gtk-4.0/gtk.css = %q", got)
	}
	for _, name := range []string{"Orchis-Construct-Dark", "Orchis-Construct-Light"} {
		if got := readFile(t, filepath.Join(home, ".themes", name, "gtk-3.0/gtk.css")); got != name+" 3" {
			t.Errorf("%s gtk-3.0/gtk.css = %q", name, got)
		}
		if _, err := os.Stat(filepath.Join(home, ".themes", name, "gnome-shell")); !os.IsNotExist(err) {
			t.Errorf("%s: the shell's theme was copied: %v", name, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("nothing of the user's was there to leave, yet: %q", out.String())
	}
}
