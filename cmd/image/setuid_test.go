package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// privilegedRoot is a root whose packages made every program the lists name setuid, as Wolfi's
// do; newuidmap's capabilities stand as a setuid bit, which a test cannot set.
func privilegedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, list := range []map[string]string{privileged, capable, unprivileged} {
		for path := range list {
			setuidFile(t, root, path)
		}
	}
	return root
}

func setuidFile(t *testing.T, root, path string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755|fs.ModeSetuid); err != nil {
		t.Fatal(err)
	}
}

func mode(t *testing.T, root, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func TestLimitPrivilegeClearsWhatTheImageDoesNotNeed(t *testing.T) {
	root := privilegedRoot(t)
	if err := limitPrivilege(root); err != nil {
		t.Fatal(err)
	}
	for path := range unprivileged {
		if m := mode(t, root, path); m&(fs.ModeSetuid|fs.ModeSetgid) != 0 || m.Perm() != 0o755 {
			t.Errorf("%s: %v, want 0755 and no setuid or setgid", path, m)
		}
	}
	for path := range privileged {
		if m := mode(t, root, path); m&fs.ModeSetuid == 0 {
			t.Errorf("%s: %v, lost its setuid bit", path, m)
		}
	}
}

// A package that makes a new program setuid fails the build until someone decides.
func TestLimitPrivilegeRefusesANewPrivilegedProgram(t *testing.T) {
	root := privilegedRoot(t)
	setuidFile(t, root, "usr/bin/newtool")
	err := limitPrivilege(root)
	if err == nil || !strings.Contains(err.Error(), "usr/bin/newtool") {
		t.Fatalf("err = %v, want one naming usr/bin/newtool", err)
	}
}

// A list that names what the root no longer makes privileged is out of date.
func TestLimitPrivilegeRefusesAStaleEntry(t *testing.T) {
	root := privilegedRoot(t)
	if err := os.Chmod(filepath.Join(root, "usr/bin/sudo"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := limitPrivilege(root)
	if err == nil || !strings.Contains(err.Error(), "usr/bin/sudo") {
		t.Fatalf("err = %v, want one naming usr/bin/sudo", err)
	}
}
