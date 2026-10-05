package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

// The file is replaced whole, with its mode, its directory made; nothing is left beside it.
func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "etc", "shadow")
	if err := WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "new\n" {
		t.Fatalf("%q %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o400 {
		t.Errorf("mode %v", fi.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left beside it: %v", entries)
	}
}
