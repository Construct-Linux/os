package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The themes reach the guest as built: their files, directories and links, named from the
// build's top.
func TestTarGz(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Dark", "gtk-4.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dark", "gtk-4.0", "gtk.css"), []byte("a{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Dark", filepath.Join(dir, "Default")); err != nil {
		t.Fatal(err)
	}
	enc, err := tarGz(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		got[h.Name] = string(rune(h.Typeflag)) + h.Linkname + string(body)
	}
	want := map[string]string{
		"Dark/":                string(rune(tar.TypeDir)),
		"Dark/gtk-4.0/":        string(rune(tar.TypeDir)),
		"Dark/gtk-4.0/gtk.css": string(rune(tar.TypeReg)) + "a{}",
		"Default":              string(rune(tar.TypeSymlink)) + "Dark",
	}
	if len(got) != len(want) {
		t.Errorf("entries %q, want %q", got, want)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: %q, want %q", n, got[n], w)
		}
	}
}
