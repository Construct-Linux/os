package account

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLaysTheRecordsSystemdReads(t *testing.T) {
	etc := t.TempDir()
	u := User{Name: "ada", RealName: "Ada Lovelace", UID: FirstUID, HashedPassword: "$y$hash", Groups: []string{"wheel", "docker"}}
	if err := Write(etc, u); err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	b, err := os.ReadFile(filepath.Join(etc, "userdb", "ada.user"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if rec["userName"] != "ada" || rec["homeDirectory"] != "/var/home/ada" || rec["uid"] != float64(1000) {
		t.Fatalf("user record: %v", rec)
	}
	if _, ok := rec["privileged"]; ok {
		t.Fatal("the public record carries the password hash")
	}
	info, err := os.Stat(filepath.Join(etc, "userdb", "ada.user-privileged"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("privileged record: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(etc, "userdb", "1000.user")); err != nil || target != "ada.user" {
		t.Fatalf("uid link: %q %v", target, err)
	}
	for _, m := range []string{"ada:wheel.membership", "ada:docker.membership"} {
		if _, err := os.Stat(filepath.Join(etc, "userdb", m)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(etc, "sudoers.d", "50-ada")); !os.IsNotExist(err) {
		t.Fatalf("a person got passwordless sudo: %v", err)
	}
	if ok, err := HasPerson(etc); err != nil || !ok {
		t.Fatalf("HasPerson after Write: %v %v", ok, err)
	}
}

func TestLiveSudoesWithoutAPassword(t *testing.T) {
	etc := t.TempDir()
	if ok, err := HasPerson(etc); err != nil || ok {
		t.Fatalf("HasPerson on an empty /etc: %v %v", ok, err)
	}
	if err := Write(etc, Live); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(etc, "sudoers.d", "50-live"))
	if err != nil || string(b) != "live ALL=(ALL:ALL) NOPASSWD: ALL\n" {
		t.Fatalf("live's sudoers: %q %v", b, err)
	}
}

func TestValidName(t *testing.T) {
	for name, ok := range map[string]bool{"ada": true, "a_b-1": true, "Ada": false, "1ada": false, "root": false, "live": false, "": false} {
		if got := ValidName(name) == nil; got != ok {
			t.Errorf("ValidName(%q) ok = %v, want %v", name, got, ok)
		}
	}
}
