// Package account is the person a Spindrift machine is for: a user record in systemd's userdb
// drop-ins (etc/userdb), GNOME logging them in at boot, and their home on /var.
//
// The image carries no person. The installer writes the one it asks for into the installed
// disk's /etc (the overlay's upper layer, on /var); a root that boots with nobody there - a live
// stick, the test disk - gets Live, whom spin-init writes. Records are files of their own beside
// the image's /etc/passwd rather than lines in a copy of it: a copy in the overlay's upper layer
// would hide every later image's passwd for good.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// User is a person who logs in.
type User struct {
	Name     string // login name
	RealName string // what GNOME shows
	UID      int    // also their own group's id
	// HashedPassword is crypt(3)'s form; "" is no password, which only Live has.
	HashedPassword string
	// Groups are the image's groups they are a member of.
	Groups []string
	// Sudo without a password: only Live, the session a stick boots into with nobody installed.
	SudoNoPassword bool
}

// FirstUID is the person's uid: the image's own users are system users, below it.
const FirstUID = 1000

// Groups are what a person of this machine is in: wheel (sudo, with their password), the devices
// a desktop session uses, and docker.
var Groups = []string{"wheel", "video", "audio", "input", "docker"}

// Live is the user a root with nobody installed boots into.
var Live = User{Name: "live", RealName: "Live session", UID: FirstUID, Groups: Groups, SudoNoPassword: true}

var validName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)

// reserved are names the image or GNOME already use.
var reserved = map[string]bool{
	"root": true, "live": true, "gdm": true, "nobody": true, "docker": true, "wheel": true,
	"audio": true, "video": true, "input": true, "avahi": true, "messagebus": true,
}

// ValidName says why name cannot be a person's login, or nil.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q: a login is lowercase letters, digits, - and _, starting with a letter, at most 31", name)
	}
	if reserved[name] {
		return fmt.Errorf("%q is taken by the system", name)
	}
	return nil
}

// Home is where a user's home is: /var holds what a machine keeps.
func Home(name string) string { return "/var/home/" + name }

// Write lays u's records under etc (an /etc, or its overlay's upper layer): the user and their
// own group, each also under its id, the memberships, and with SudoNoPassword their sudoers line.
// The password hash goes into a companion file only root reads, as systemd reads it back.
func Write(etc string, u User) error {
	if err := ValidName(u.Name); err != nil && u.Name != Live.Name {
		return err
	}
	dir := filepath.Join(etc, "userdb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	user := map[string]any{
		"userName":      u.Name,
		"realName":      u.RealName,
		"uid":           u.UID,
		"gid":           u.UID,
		"homeDirectory": Home(u.Name),
		"shell":         "/bin/bash",
		"disposition":   "regular",
	}
	privileged := map[string]any{"privileged": map[string]any{"hashedPassword": hashes(u.HashedPassword)}}
	group := map[string]any{"groupName": u.Name, "gid": u.UID, "disposition": "regular"}
	for _, f := range []struct {
		name string
		v    any
		mode os.FileMode
	}{
		{u.Name + ".user", user, 0o644},
		{u.Name + ".user-privileged", privileged, 0o600},
		{u.Name + ".group", group, 0o644},
	} {
		b, err := json.MarshalIndent(f.v, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, f.name), append(b, '\n'), f.mode); err != nil {
			return err
		}
	}
	for link, target := range map[string]string{
		fmt.Sprintf("%d.user", u.UID):            u.Name + ".user",
		fmt.Sprintf("%d.user-privileged", u.UID): u.Name + ".user-privileged",
		fmt.Sprintf("%d.group", u.UID):           u.Name + ".group",
	} {
		p := filepath.Join(dir, link)
		_ = os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			return err
		}
	}
	for _, g := range u.Groups {
		if err := writeFile(filepath.Join(dir, u.Name+":"+g+".membership"), nil, 0o644); err != nil {
			return err
		}
	}
	if u.SudoNoPassword {
		line := u.Name + " ALL=(ALL:ALL) NOPASSWD: ALL\n"
		if err := writeFile(filepath.Join(etc, "sudoers.d", "50-"+u.Name), []byte(line), 0o440); err != nil {
			return err
		}
	}
	return nil
}

// hashes is a user record's hashedPassword: none for no password.
func hashes(h string) []string {
	if h == "" {
		return []string{}
	}
	return []string{h}
}

// Autologin makes GDM log name in at boot. The password that unlocks /var at boot is theirs, so
// the session needs no second one; the lock screen asks for it.
func Autologin(etc, name string) error {
	conf := "# The person this machine is for, logged in at boot: unlocking /var took their password.\n" +
		"[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=" + name + "\n"
	return writeFile(filepath.Join(etc, "gdm", "custom.conf"), []byte(conf), 0o644)
}

// MakeHome makes u's home on varDir, where a /var is mounted, theirs.
func MakeHome(varDir string, u User) error {
	home := filepath.Join(varDir, strings.TrimPrefix(Home(u.Name), "/var/"))
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(home, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return os.Chown(home, u.UID, u.UID)
}

// HasPerson says whether etc has a person's record: a user of FirstUID or above.
func HasPerson(etc string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(etc, "userdb"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".user") || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(etc, "userdb", e.Name()))
		if err != nil {
			return false, err
		}
		var r struct {
			UID int `json:"uid"`
		}
		if json.Unmarshal(b, &r) == nil && r.UID >= FirstUID {
			return true, nil
		}
	}
	return false, nil
}

// writeFile writes b to path, with its directory, synced: a power cut after an install must not
// leave a person's record empty.
func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
