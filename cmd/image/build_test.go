package main

import (
	"archive/tar"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/spin-stack/spindrift/internal/update"
)

// The transfers systemd-sysupdate runs match the files a release is written with: what the build
// names (update.Names) is what an update looks for, the root and tree by version and partition
// UUID, the kernel image by version.
func TestTransfersMatchTheReleaseNames(t *testing.T) {
	const version = "20261002.090000"
	const rootUUID, verityUUID = "0123abcd-0000-4000-8000-000000000001", "0123abcd-0000-4000-8000-000000000002"
	rootFile, verityFile, uki := update.Names(version, rootUUID, verityUUID)
	pattern := regexp.MustCompile(`(?m)^\[Source\][^\[]*MatchPattern=(\S+)`)
	want := map[string]string{
		"20-root.transfer":        rootFile,
		"10-root-verity.transfer": verityFile,
		"90-uki.transfer":         uki,
	}
	got := transfers("https://example.org/r")
	if len(got) != len(want) {
		t.Fatalf("%d transfers, want %d", len(got), len(want))
	}
	for name, file := range want {
		m := pattern.FindStringSubmatch(got[name])
		if m == nil {
			t.Fatalf("%s has no source MatchPattern", name)
		}
		uuid := rootUUID
		if strings.Contains(name, "verity") {
			uuid = verityUUID
		}
		if p := strings.NewReplacer("@v", version, "@u", uuid).Replace(m[1]); p != file {
			t.Errorf("%s looks for %s; the release has %s", name, p, file)
		}
	}
}

// Every source is url-file: only those have systemd-sysupdate check SHA256SUMS.gpg and each
// file's digest; a regular-file source is written unchecked. And the kernel image stays
// writable, or systemd-boot cannot count its tries.
func TestTransfersAreChecked(t *testing.T) {
	for _, url := range []string{"https://example.org/r", "file:///run/spindrift/update"} {
		for name, body := range transfers(url) {
			src := body[strings.Index(body, "[Source]"):strings.Index(body, "[Target]")]
			if !strings.Contains(src, "Type=url-file\n") || !strings.Contains(src, "Path="+url+"\n") {
				t.Errorf("%s from %s: source is not url-file from it:\n%s", name, url, src)
			}
		}
		if uki := transfers(url)["90-uki.transfer"]; !strings.Contains(uki, "Mode=0644\n") || !strings.Contains(uki, "TriesLeft=3\n") {
			t.Errorf("the kernel image transfer is not 0644 with three tries:\n%s", uki)
		}
	}
}

// A later layer's file replaces an earlier one's; a directory keeps the first layer's header and
// comes before its files; a symlink stays a symlink.
func TestMergeLaysLaterLayersOver(t *testing.T) {
	a := tree(t, map[string]string{"etc/os-release": "base", "etc/keep": "a", "usr/bin/sh": "->busybox"})
	b := tree(t, map[string]string{"etc/os-release": "overlay", "etc/new": "b"})
	dst := filepath.Join(t.TempDir(), "root.tar")
	if err := merge([]layer{dirLayer{dir: a}, dirLayer{dir: b}}, dst); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	seen := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[h.Name]; dup {
			t.Errorf("%s is in the tar twice", h.Name)
		}
		if dir := filepathDir(h.Name); dir != "." && seen[dir+"/"] != "dir" {
			t.Errorf("%s comes before its directory", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			seen[h.Name] = "dir"
		case tar.TypeSymlink:
			seen[h.Name] = "->" + h.Linkname
		default:
			b, _ := io.ReadAll(tr)
			seen[h.Name] = string(b)
		}
	}
	for name, want := range map[string]string{"etc/os-release": "overlay", "etc/keep": "a", "etc/new": "b", "usr/bin/sh": "->busybox"} {
		if seen[name] != want {
			t.Errorf("%s = %q, want %q", name, seen[name], want)
		}
	}
}

func filepathDir(name string) string { return filepath.Dir(strings.TrimSuffix(name, "/")) }

// QEMU's screendump, a binary P6 PPM with a comment, becomes a PNG of the same pixels.
func TestPPMToPNG(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s.ppm"), filepath.Join(dir, "s.png")
	ppm := "P6\n# QEMU\n2 1\n255\n" + string([]byte{255, 0, 0, 0, 0, 255})
	if err := os.WriteFile(src, []byte(ppm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ppmToPNG(src, dst); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("bounds %v", b)
	}
	if r, _, b, _ := img.At(0, 0).RGBA(); r>>8 != 255 || b != 0 {
		t.Errorf("pixel 0 is not red")
	}
	if r, _, b, _ := img.At(1, 0).RGBA(); r != 0 || b>>8 != 255 {
		t.Errorf("pixel 1 is not blue")
	}
}

// The build fails unless root has no password: on a read-only root nothing could remove one.
func TestRootLocked(t *testing.T) {
	for shadow, ok := range map[string]bool{
		"root:*::0:::::\nlive:!::0:::::\n":             true,
		"root:!::0:::::\n":                             false,
		"root:$y$j9T$abc$def::0:::::\n":                false,
		"bin:*::0:::::\n":                              false,
		"live:*::0:::::\nroot::::0:::::\nother:*:::\n": false,
	} {
		root := tree(t, map[string]string{"etc/shadow": shadow})
		if err := rootLocked(root); (err == nil) != ok {
			t.Errorf("shadow %q: rootLocked = %v, want ok %v", shadow, err, ok)
		}
	}
}

// The kernel's command line names the root by its hash alone, and options given for one build
// come last, with no stray space when there are none.
func TestCmdline(t *testing.T) {
	v := verity{rootHash: strings.Repeat("ab", 32)}
	plain := v.cmdline("")
	if !strings.HasPrefix(plain, "roothash="+v.rootHash+" ") || strings.HasSuffix(plain, " ") {
		t.Errorf("cmdline = %q", plain)
	}
	if got := v.cmdline("plymouth.debug"); got != plain+" plymouth.debug" {
		t.Errorf("cmdline with an option = %q", got)
	}
}

// A package's translations are missing when its -lang package exists and is not installed; a
// -lang package of something not installed, or one already installed, is not.
func TestMissingLangs(t *testing.T) {
	installed := map[string]bool{"gnome-shell": true, "gtk-4": true, "gtk-4-lang": true, "bash": true}
	available := map[string]bool{"gnome-shell-lang": true, "gtk-4-lang": true, "nautilus-lang": true}
	got := missingLangs(installed, available)
	if len(got) != 1 || got[0] != "gnome-shell-lang" {
		t.Errorf("missingLangs = %q, want [gnome-shell-lang]", got)
	}
}

// The names come from an apk database or index's P: lines alone.
func TestPackageNames(t *testing.T) {
	names := map[string]bool{}
	db := "C:Q1abc=\nP:glib\nV:2.90.0-r0\nD:so:libc.so.6\n\nP:glib-lang\nV:2.90.0-r0\n"
	if err := packageNames(strings.NewReader(db), names); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || !names["glib"] || !names["glib-lang"] {
		t.Errorf("names = %v", names)
	}
}

// A dropped package's files go, by the apk database's F:/R: lines; another package's stay.
func TestDropPackages(t *testing.T) {
	root := tree(t, map[string]string{
		"usr/lib/apk/db/installed": "P:curl\nV:8-r0\nF:usr/bin\nR:curl\n\nP:bash\nV:5-r0\nF:usr/bin\nR:bash\n",
		"usr/bin/curl":             "x",
		"usr/bin/bash":             "y",
	})
	if err := dropPackages(root, []string{"curl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/bin/curl")); !os.IsNotExist(err) {
		t.Errorf("curl is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr/bin/bash")); err != nil {
		t.Errorf("bash went too: %v", err)
	}
}

// Files with the same contents and mode become one inode; a file that differs stays its own.
func TestLinkDuplicates(t *testing.T) {
	root := tree(t, map[string]string{"a/lib.so": "same", "b/lib.so": "same", "c/other.so": "different"})
	if err := linkDuplicates(root); err != nil {
		t.Fatal(err)
	}
	ino := func(p string) uint64 {
		fi, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Sys().(*syscall.Stat_t).Ino
	}
	if ino("a/lib.so") != ino("b/lib.so") {
		t.Error("identical files were not linked")
	}
	if ino("c/other.so") == ino("a/lib.so") {
		t.Error("a different file was linked")
	}
}
