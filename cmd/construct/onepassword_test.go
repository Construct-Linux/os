package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

type entry struct {
	name, link string
	dir        bool
	mode       int64
}

func tarball(t *testing.T, entries []entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			h.Typeflag = tar.TypeDir
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.name))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.name))
		}
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "1password.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The one top directory is stripped, its name gives the version, and no setuid bit survives.
func TestExtractTop(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "1Password")
	version, err := extractTop(tarball(t, []entry{
		{name: "1password-8.12.38.x64/", dir: true, mode: 0o755},
		{name: "1password-8.12.38.x64/1password", mode: 0o755},
		{name: "1password-8.12.38.x64/chrome-sandbox", mode: 0o4755},
		{name: "1password-8.12.38.x64/resources/app.asar", mode: 0o644},
		{name: "1password-8.12.38.x64/libffmpeg.so.1", link: "libffmpeg.so"},
	}), dest)
	if err != nil || version != "8.12.38" {
		t.Fatalf("%q %v", version, err)
	}
	fi, err := os.Stat(filepath.Join(dest, "chrome-sandbox"))
	if err != nil || fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("chrome-sandbox %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(dest, "resources", "app.asar")); err != nil {
		t.Error(err)
	}
}

// Nothing is written outside the tree: neither a name with .. nor a link that leaves it.
func TestExtractTopStaysInside(t *testing.T) {
	for _, bad := range [][]entry{
		{{name: "1password-1.0.x64/1password", mode: 0o755}, {name: "1password-1.0.x64/../../escape", mode: 0o644}},
		{{name: "1password-1.0.x64/1password", mode: 0o755}, {name: "1password-1.0.x64/lib", link: "../../etc"}},
		{{name: "1password-1.0.x64/1password", mode: 0o755}, {name: "1password-1.0.x64/abs", link: "/etc/shadow"}},
		{{name: "1password-1.0.x64/1password", mode: 0o755}, {name: "other/1password", mode: 0o755}},
	} {
		dir := t.TempDir()
		if _, err := extractTop(tarball(t, bad), filepath.Join(dir, "1Password")); err == nil {
			t.Errorf("%+v extracted", bad)
		}
		if _, err := os.Stat(filepath.Join(dir, "escape")); err == nil {
			t.Error("a file escaped the tree")
		}
	}
}

// The tarball's signature is checked against the key; another key's signature fails it, and a
// keyring that is not the pinned key is refused before anything is downloaded.
func TestOnePasswordSignature(t *testing.T) {
	signer, err := openpgp.NewEntity("1Password test", "", "test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := openpgp.NewEntity("Other", "", "other@example.com", nil)
	dir := t.TempDir()
	file, sig := filepath.Join(dir, "f.tar.gz"), filepath.Join(dir, "f.tar.gz.sig")
	if err := os.WriteFile(file, []byte("the tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	var s bytes.Buffer
	if err := openpgp.DetachSign(&s, signer, strings.NewReader("the tarball"), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sig, s.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkDetached(openpgp.EntityList{signer}, file, sig); err != nil {
		t.Errorf("good signature: %v", err)
	}
	if err := checkDetached(openpgp.EntityList{other}, file, sig); err == nil {
		t.Error("another key's signature passed")
	}
	var pub bytes.Buffer
	w, _ := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	_ = signer.Serialize(w)
	w.Close()
	key := filepath.Join(dir, "1password.asc")
	if err := os.WriteFile(key, pub.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := onePasswordKeyring(key); err == nil || !strings.Contains(err.Error(), onePasswordFingerprint) {
		t.Errorf("a key that is not the pinned one: %v", err)
	}
}

// The key the image carries is 1Password's own, the one pinned.
func TestOnePasswordKeyInTheImage(t *testing.T) {
	if _, err := onePasswordKeyring("../../image/rootfs/usr/share/construct/1password.asc"); err != nil {
		t.Error(err)
	}
}
