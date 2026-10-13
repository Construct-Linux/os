// Package account is the person a Construct machine is for: a user in /etc/passwd and
// /etc/shadow, in their groups, GNOME logging them in at boot or not, and their home on /var.
//
// The image carries no person. The installer writes the one it asks for into the installed
// disk's /etc (the overlay's upper layer, on /var); a root that boots with nobody there - a live
// stick, the test disk - gets Live, whom construct live writes. They go where passwd, chpasswd,
// accountsservice and pam_unix change them: the classic files, which a person's /etc keeps a
// copy of from then on. The image's own users still reach that copy - systemd-sysusers adds
// what a new release declares at every boot (image/rootfs's systemd-sysusers.service.d) - and
// the image's untouched files are under usr/share/factory/etc (cmd/image's bake), what an
// install starts from.
package account

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Construct-Linux/os/internal/fsx"
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

// Groups are what a person of this machine is in: wheel (sudo, with their password), docker, and
// lpadmin (CUPS's printer administration) - what accountsservice gives an administrator too
// (recipes/accountsservice, extra_admin_groups). logind gives the session its devices.
var Groups = []string{"wheel", "docker", "lpadmin"}

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

// Free says why name cannot be a person's login on a machine whose image's account files are
// in factory, or nil: a valid name that no user or group of the image has. An installer asks
// it before anything is written to the disk; Write refuses the same names.
func Free(factory, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	for _, db := range []string{"passwd", "group"} {
		b, err := os.ReadFile(filepath.Join(factory, db))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if _, ok := idOf(strings.Split(string(b), "\n"), name); ok {
			return fmt.Errorf("%q is taken by the system", name)
		}
	}
	return nil
}

// ValidRealName says why s cannot be a person's full name, or nil: passwd's fields are split at
// colons and its records at newlines, so a name with either would write other fields, or lines.
func ValidRealName(s string) error {
	if strings.ContainsFunc(s, func(r rune) bool { return r == ':' || r == ',' || unicode.IsControl(r) }) {
		return fmt.Errorf("%q: a full name has no colon, comma or control character", s)
	}
	return nil
}

// idOf is the id an account file's line for name carries - a user's uid, a group's gid - and
// whether there is one.
func idOf(lines []string, name string) (string, bool) {
	for _, line := range lines {
		if f := strings.Split(line, ":"); len(f) > 2 && f[0] == name {
			return f[2], true
		}
	}
	return "", false
}

// Shell is a person's login shell.
const Shell = "/bin/bash"

// home is where a user's home is: /var holds what a machine keeps.
func home(name string) string { return "/var/home/" + name }

// databases are the account files a user is written into.
var databases = []string{"passwd", "group", "shadow", "gshadow"}

// Write adds u to the account files under etc (an /etc, or its overlay's upper layer) - taking
// each from factory, the image's untouched copies, where etc has none yet - with their own
// group, a member of u.Groups, and with SudoNoPassword their sudoers line. A user already there
// is written again; a name the system's users or groups have is refused, before any file is
// written.
func Write(etc, factory string, u User) error {
	if err := ValidName(u.Name); err != nil && u.Name != Live.Name {
		return err
	}
	if err := ValidRealName(u.RealName); err != nil {
		return err
	}
	uid := strconv.Itoa(u.UID)
	days := strconv.FormatInt(time.Now().Unix()/86400, 10)
	own := map[string]string{
		"passwd":  strings.Join([]string{u.Name, "x", uid, uid, u.RealName, home(u.Name), Shell}, ":"),
		"group":   u.Name + ":x:" + uid + ":",
		"shadow":  strings.Join([]string{u.Name, u.HashedPassword, days, "0", "99999", "7", "", "", ""}, ":"),
		"gshadow": u.Name + ":!::",
	}
	type file struct {
		lines []string
		mode  fs.FileMode
	}
	files := map[string]file{}
	for _, db := range databases {
		src := filepath.Join(etc, db)
		fi, err := os.Stat(src)
		if errors.Is(err, fs.ErrNotExist) {
			src = filepath.Join(factory, db)
			fi, err = os.Stat(src)
		}
		if err != nil {
			return err
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		files[db] = file{strings.Split(strings.TrimRight(string(b), "\n"), "\n"), fi.Mode().Perm()}
	}
	// Their own line is replaced; anyone else's - a system user or group of the same name, with
	// another id - would be lost with it.
	for _, db := range []string{"passwd", "group"} {
		if id, ok := idOf(files[db].lines, u.Name); ok && id != uid {
			return fmt.Errorf("%q is taken by the system", u.Name)
		}
	}
	for _, g := range u.Groups {
		if _, ok := idOf(files["group"].lines, g); !ok {
			return fmt.Errorf("account: the image has no group %s", g)
		}
	}
	for _, db := range databases {
		out := withUser(files[db].lines, u.Name, db == "group" || db == "gshadow", u.Groups)
		out = append(out, own[db])
		if err := fsx.WriteFile(filepath.Join(etc, db), []byte(strings.Join(out, "\n")+"\n"), files[db].mode); err != nil {
			return err
		}
	}
	if u.SudoNoPassword {
		line := u.Name + " ALL=(ALL:ALL) NOPASSWD: ALL\n"
		if err := fsx.WriteFile(filepath.Join(etc, "sudoers.d", "50-"+u.Name), []byte(line), 0o440); err != nil {
			return err
		}
	}
	return nil
}

// withUser is an account file's lines without name's own line, and - in a group file, where a
// group's members are its last field - with name added to each of groups.
func withUser(lines []string, name string, isGroups bool, groups []string) []string {
	var out []string
	for _, line := range lines {
		f := strings.Split(line, ":")
		if line == "" || f[0] == name {
			continue
		}
		if isGroups && slices.Contains(groups, f[0]) {
			members := strings.FieldsFunc(f[len(f)-1], func(r rune) bool { return r == ',' })
			if !slices.Contains(members, name) {
				members = append(members, name)
			}
			f[len(f)-1] = strings.Join(members, ",")
			line = strings.Join(f, ":")
		}
		out = append(out, line)
	}
	return out
}

// Autologin makes GDM log name in at boot: the live user, which has nothing of anyone's to
// protect, or the person whose password opened the encrypted /var moments before (cmd/construct).
func Autologin(etc, name string) error {
	conf := "# Logged in at boot: the live session, or the person whose password opened /var.\n" +
		"[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=" + name + "\n"
	return fsx.WriteFile(filepath.Join(etc, "gdm", "custom.conf"), []byte(conf), 0o644)
}

// MakeHome makes u's home on varDir, where a /var is mounted, theirs.
func MakeHome(varDir string, u User) error {
	dir := filepath.Join(varDir, strings.TrimPrefix(home(u.Name), "/var/"))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return os.Chown(dir, u.UID, u.UID)
}

// HasPerson says whether etc's passwd has a person in it: a user in systemd's range of regular
// ones, FirstUID up to 60000 (nobody, 65534, is not one).
func HasPerson(etc string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(etc, "passwd"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Split(line, ":"); len(f) > 2 {
			if uid, err := strconv.Atoi(f[2]); err == nil && uid >= FirstUID && uid < 60000 {
				return true, nil
			}
		}
	}
	return false, nil
}
