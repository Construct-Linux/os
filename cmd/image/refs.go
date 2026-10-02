package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// refs checks that what the baked root's configuration names exists in it: the units it runs
// (enabled, or started by the bus) and the programs, units, users and groups they name; the PAM
// files and modules a PAM file includes; the users and groups tmpfiles and udev hand files to.
// A package built without its unit, a PAM file in another distribution's dialect, a group nobody
// declares: each of these booted, logged nothing at error level, and broke something - Bluetooth,
// polkit, sshd's keys - until a person noticed. The check makes the build notice.
func refs(root string) []string {
	r := refChecker{root: root, users: names(root, "etc/passwd"), groups: names(root, "etc/group"),
		aliases: declaredAliases(root)}
	r.units()
	r.dbus()
	r.pam()
	r.owners()
	sort.Strings(r.problems)
	return slices.Compact(r.problems)
}

type refChecker struct {
	root          string
	users, groups map[string]bool
	// aliases are the names units declare in [Install] Alias=: they exist once the unit is enabled.
	aliases  map[string]bool
	problems []string
}

func (r *refChecker) fail(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

func (r *refChecker) exists(p string) bool {
	_, err := os.Stat(r.resolve(p))
	return err == nil
}

// resolve follows p's links inside the root: an absolute link target is the root's, not the host's.
func (r *refChecker) resolve(p string) string {
	p = clean(p)
	for range 40 {
		fi, err := os.Lstat(filepath.Join(r.root, p))
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			break
		}
		t, err := os.Readlink(filepath.Join(r.root, p))
		if err != nil {
			break
		}
		if !path.IsAbs(t) {
			t = path.Join(path.Dir("/"+p), t)
		}
		p = clean(t)
	}
	return filepath.Join(r.root, p)
}

// unitDirs are where systemd looks for units, in its order; a unit's name resolves in the first.
var unitDirs = map[string][]string{
	"system": {"etc/systemd/system", "usr/lib/systemd/system"},
	"user":   {"etc/systemd/user", "usr/lib/systemd/user"},
}

// unitFile is the file a unit name resolves to for scope, or "".
func (r *refChecker) unitFile(scope, name string) string {
	for _, d := range unitDirs[scope] {
		if p := filepath.Join(d, name); r.exists(p) {
			return p
		}
		// An instance of a template: foo@bar.service is foo@.service's.
		if at := strings.Index(name, "@"); at > 0 {
			if p := filepath.Join(d, name[:at+1]+name[strings.LastIndex(name, "."):]); r.exists(p) {
				return p
			}
		}
	}
	return ""
}

// units checks every link that enables a unit or names it by alias, and the programs and users
// of the units those links run.
func (r *refChecker) units() {
	for scope, dirs := range unitDirs {
		for _, d := range dirs {
			_ = filepath.WalkDir(filepath.Join(r.root, d), func(p string, e fs.DirEntry, err error) error {
				if err != nil || e.Type()&fs.ModeSymlink == 0 {
					return nil
				}
				rel, _ := filepath.Rel(r.root, p)
				t, _ := os.Readlink(p)
				if t == "/dev/null" {
					return nil // masked
				}
				if !r.exists(rel) {
					r.fail("%s links to %s, which is not in the root", rel, t)
					return nil
				}
				r.unit(scope, r.resolve(rel))
				return nil
			})
		}
	}
}

// unit checks one unit file's programs and the users it runs as.
func (r *refChecker) unit(scope, file string) {
	rel, _ := filepath.Rel(r.root, file)
	for _, kv := range keys(file) {
		switch {
		case strings.HasPrefix(kv[0], "Exec"):
			if prog := program(kv[1]); prog != "" && !r.program(prog) {
				r.fail("%s: %s=%s: no such program", rel, kv[0], prog)
			}
		case kv[0] == "User" && scope == "system" && !r.users[kv[1]] && !dynamic(file):
			r.fail("%s: User=%s is declared nowhere", rel, kv[1])
		case kv[0] == "Group" && scope == "system" && !r.groups[kv[1]] && !dynamic(file):
			r.fail("%s: Group=%s is declared nowhere", rel, kv[1])
		}
	}
}

// dbus checks the system bus's activatable services: the program, the user, the systemd unit.
func (r *refChecker) dbus() {
	dir := filepath.Join(r.root, "usr/share/dbus-1/system-services")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		file := filepath.Join(dir, e.Name())
		rel := "usr/share/dbus-1/system-services/" + e.Name()
		for _, kv := range keys(file) {
			switch kv[0] {
			case "Exec":
				if prog := program(kv[1]); prog != "" && prog != "/bin/false" && !r.program(prog) {
					r.fail("%s: Exec=%s: no such program", rel, prog)
				}
			case "User":
				if !r.users[kv[1]] {
					r.fail("%s: User=%s is declared nowhere", rel, kv[1])
				}
			case "SystemdService":
				// A unit the presets leave off (networkd, oomd) is not started by the bus either, as
				// meant; a name no unit answers to is a package that came without its unit.
				f := r.unitFile("system", kv[1])
				if f == "" && !r.aliases[kv[1]] {
					r.fail("%s: SystemdService=%s: no unit is or declares it", rel, kv[1])
					continue
				}
				if f != "" {
					r.unit("system", r.resolve(f))
				}
			}
		}
	}
}

// pam checks every PAM file's includes and the modules it requires; a "-" line is optional.
func (r *refChecker) pam() {
	for _, d := range []string{"etc/pam.d", "usr/lib/pam.d"} {
		entries, _ := os.ReadDir(filepath.Join(r.root, d))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			rel := d + "/" + e.Name()
			f, err := os.Open(r.resolve(rel))
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				w := strings.Fields(strings.SplitN(sc.Text(), "#", 2)[0])
				if len(w) < 3 || strings.HasPrefix(w[0], "-") {
					continue
				}
				switch {
				case w[1] == "include" || w[1] == "substack":
					if !r.exists("etc/pam.d/"+w[2]) && !r.exists("usr/lib/pam.d/"+w[2]) {
						r.fail("%s: includes %s, which is not in the root", rel, w[2])
					}
				case strings.HasSuffix(w[2], ".so"):
					mod := w[2]
					if !path.IsAbs(mod) {
						mod = "usr/lib/security/" + mod
					}
					if !r.exists(mod) {
						r.fail("%s: needs %s, which is not in the root", rel, w[2])
					}
				}
			}
			_ = f.Close()
		}
	}
}

// owners checks the users and groups tmpfiles.d and udev rules give files to.
func (r *refChecker) owners() {
	for _, d := range []string{"usr/lib/tmpfiles.d", "etc/tmpfiles.d"} {
		entries, _ := os.ReadDir(filepath.Join(r.root, d))
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			rel := d + "/" + e.Name()
			eachLine(r.resolve(rel), func(line string) {
				w := strings.Fields(line)
				if len(w) < 5 {
					return
				}
				if u := w[3]; named(u) && !r.users[u] {
					r.fail("%s: user %s is declared nowhere", rel, u)
				}
				if g := w[4]; named(g) && !r.groups[g] {
					r.fail("%s: group %s is declared nowhere", rel, g)
				}
			})
		}
	}
	for _, d := range []string{"usr/lib/udev/rules.d", "etc/udev/rules.d"} {
		entries, _ := os.ReadDir(filepath.Join(r.root, d))
		for _, e := range entries {
			rel := d + "/" + e.Name()
			eachLine(r.resolve(rel), func(line string) {
				for _, f := range strings.Split(line, ",") {
					f = strings.TrimSpace(f)
					for key, known := range map[string]map[string]bool{"OWNER": r.users, "GROUP": r.groups} {
						if v, ok := strings.CutPrefix(f, key+"="); ok || strings.HasPrefix(f, key+":=") {
							if !ok {
								v = strings.TrimPrefix(f, key+":=")
							}
							v = strings.Trim(v, `"`)
							if v != "" && !strings.Contains(v, "$") && !known[v] {
								r.fail("%s: %s %s is declared nowhere", rel, strings.ToLower(key), v)
							}
						}
					}
				}
			})
		}
	}
}

// program is whether name - absolute, or a bare name systemd looks up in its PATH - is a program
// in the root.
func (r *refChecker) program(name string) bool {
	if path.IsAbs(name) {
		return r.exists(name)
	}
	for _, d := range []string{"usr/local/bin", "usr/bin", "usr/lib/systemd"} {
		if r.exists(d + "/" + name) {
			return true
		}
	}
	return false
}

// program is the program an Exec= line runs: its first word, past systemd's prefixes (-@:+!).
func program(line string) string {
	w := strings.Fields(line)
	if len(w) == 0 {
		return ""
	}
	p := strings.TrimLeft(w[0], "-@:+!|")
	if strings.HasPrefix(p, "$") || p == "" {
		return ""
	}
	return p
}

// named is whether a tmpfiles owner field names a user or group: not "-", a specifier, or a number.
func named(v string) bool {
	if v == "-" || strings.ContainsAny(v, "%:") {
		return false
	}
	return strings.Trim(v, "0123456789") != ""
}

// declaredAliases are the Alias= names every unit file in the root declares.
func declaredAliases(root string) map[string]bool {
	m := map[string]bool{}
	for _, dirs := range unitDirs {
		for _, d := range dirs {
			entries, _ := os.ReadDir(filepath.Join(root, d))
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				for _, kv := range keys(filepath.Join(root, d, e.Name())) {
					if kv[0] == "Alias" {
						for _, a := range strings.Fields(kv[1]) {
							m[a] = true
						}
					}
				}
			}
		}
	}
	return m
}

// dynamic is whether a unit runs as a user systemd makes for it.
func dynamic(file string) bool {
	for _, kv := range keys(file) {
		if kv[0] == "DynamicUser" && (kv[1] == "yes" || kv[1] == "true" || kv[1] == "1") {
			return true
		}
	}
	return false
}

// keys are an ini-style file's key=value lines, in order.
func keys(file string) [][2]string {
	var out [][2]string
	eachLine(file, func(line string) {
		if k, v, ok := strings.Cut(line, "="); ok {
			out = append(out, [2]string{strings.TrimSpace(k), strings.TrimSpace(v)})
		}
	})
	return out
}

// eachLine calls fn with each line of file that is not empty or a comment.
func eachLine(file string, fn func(string)) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		fn(line)
	}
}

// names are the names in the first field of a passwd or group file.
func names(root, file string) map[string]bool {
	m := map[string]bool{}
	eachLine(filepath.Join(root, file), func(line string) {
		if n, _, ok := strings.Cut(line, ":"); ok {
			m[n] = true
		}
	})
	return m
}
