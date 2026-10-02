package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// factory is an image's account files, as bake leaves them under usr/share/factory/etc.
func factory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"passwd":  "root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534:nobody:/:/sbin/nologin\n",
		"group":   "root:x:0:\nwheel:x:10:\ndocker:x:990:old\n",
		"shadow":  "root:*::0:::::\n",
		"gshadow": "root:!::\nwheel:!::\ndocker:!::old\n",
	} {
		mode := os.FileMode(0o644)
		// The image's are 0000, which only root reads; a mode of their own is what is checked.
		if strings.Contains(name, "shadow") {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A person goes into an empty upper /etc as passwd and shadow lines over the image's files, in
// their own group and the image's groups, the shadow files keeping the image's mode.
func TestWriteAddsThePersonToTheImagesFiles(t *testing.T) {
	etc, fac := t.TempDir(), factory(t)
	u := User{Name: "ada", RealName: "Ada Lovelace", UID: FirstUID, HashedPassword: "$y$hash", Groups: []string{"wheel", "docker"}}
	if ok, err := HasPerson(etc); err != nil || ok {
		t.Fatalf("HasPerson on an empty /etc: %v %v", ok, err)
	}
	if err := Write(etc, fac, u); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(etc, "passwd")); !strings.HasPrefix(got, "root:x:0:0:root:/root:/bin/bash\n") || !strings.HasSuffix(got, "ada:x:1000:1000:Ada Lovelace:/var/home/ada:/bin/bash\n") {
		t.Errorf("passwd:\n%s", got)
	}
	if got := read(t, filepath.Join(etc, "group")); got != "root:x:0:\nwheel:x:10:ada\ndocker:x:990:old,ada\nada:x:1000:\n" {
		t.Errorf("group:\n%s", got)
	}
	if got := read(t, filepath.Join(etc, "gshadow")); got != "root:!::\nwheel:!::ada\ndocker:!::old,ada\nada:!::\n" {
		t.Errorf("gshadow:\n%s", got)
	}
	shadow := read(t, filepath.Join(etc, "shadow"))
	if !strings.HasPrefix(shadow, "root:*::0:::::\nada:$y$hash:") {
		t.Errorf("shadow:\n%s", shadow)
	}
	if fi, err := os.Stat(filepath.Join(etc, "shadow")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("shadow's mode: %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(etc, "sudoers.d", "50-ada")); !os.IsNotExist(err) {
		t.Fatalf("a person got passwordless sudo: %v", err)
	}
	if ok, err := HasPerson(etc); err != nil || !ok {
		t.Fatalf("HasPerson after Write: %v %v", ok, err)
	}
	// Written again - drift live at the next boot, an install retried - it is there once.
	if err := Write(etc, fac, u); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(etc, "group")); strings.Count(got, "ada") != 3 {
		t.Errorf("group after a second Write:\n%s", got)
	}
}

// A group the image does not have is refused, not made up.
func TestWriteRefusesAGroupTheImageLacks(t *testing.T) {
	u := User{Name: "ada", UID: FirstUID, Groups: []string{"plugdev"}}
	if err := Write(t.TempDir(), factory(t), u); err == nil {
		t.Fatal("Write took a group the image has not")
	}
}

func TestLiveSudoesWithoutAPassword(t *testing.T) {
	etc := t.TempDir()
	if err := Write(etc, factory(t), User{Name: Live.Name, UID: FirstUID, SudoNoPassword: true}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(etc, "sudoers.d", "50-live")); got != "live ALL=(ALL:ALL) NOPASSWD: ALL\n" {
		t.Fatalf("live's sudoers: %q", got)
	}
	if !strings.Contains(read(t, filepath.Join(etc, "shadow")), "\nlive::") {
		t.Error("live has a password")
	}
}

func TestValidName(t *testing.T) {
	for name, ok := range map[string]bool{"ada": true, "a_b-1": true, "Ada": false, "1ada": false, "root": false, "live": false, "": false} {
		if got := ValidName(name) == nil; got != ok {
			t.Errorf("ValidName(%q) ok = %v, want %v", name, got, ok)
		}
	}
}
