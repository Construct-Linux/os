package main

import (
	"os"
	"path/filepath"
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

func TestSyncGTK4FollowsTheImage(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "gtk-4.0")
	writeTree(t, src, map[string]string{"gtk.css": "v1", "assets/a.svg": "a", "assets/old.svg": "old"})
	if ok, err := syncGTK4(src, dst); err != nil || !ok {
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
	if ok, err := syncGTK4(src, dst); err != nil || !ok {
		t.Fatalf("second sync: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "v2" {
		t.Fatalf("gtk.css after update = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "assets/old.svg")); !os.IsNotExist(err) {
		t.Fatalf("an asset the image dropped is still there: %v", err)
	}
}

func TestSyncGTK4LeavesTheUsersTheme(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "gtk-4.0")
	writeTree(t, src, map[string]string{"gtk.css": "v1"})
	if _, err := syncGTK4(src, dst); err != nil {
		t.Fatal(err)
	}
	// The user puts their own theme there.
	writeTree(t, dst, map[string]string{"gtk.css": "mine"})
	writeTree(t, src, map[string]string{"gtk.css": "v2"})
	if ok, err := syncGTK4(src, dst); err != nil || ok {
		t.Fatalf("sync over the user's theme: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "mine" {
		t.Fatalf("the user's gtk.css was replaced: %q", got)
	}
}

func TestSyncGTK4LeavesALinkedTheme(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"gtk.css": "v1"})
	other := filepath.Join(t.TempDir(), "gtk.css")
	writeTree(t, filepath.Dir(other), map[string]string{"gtk.css": "linked"})
	// What Orchis's own install.sh -l leaves: a link, and no stamp.
	if err := os.Symlink(other, filepath.Join(dst, "gtk.css")); err != nil {
		t.Fatal(err)
	}
	if ok, err := syncGTK4(src, dst); err != nil || ok {
		t.Fatalf("sync over a linked theme: %v %v", ok, err)
	}
	if got := readFile(t, filepath.Join(dst, "gtk.css")); got != "linked" {
		t.Fatalf("the linked gtk.css changed: %q", got)
	}
}
