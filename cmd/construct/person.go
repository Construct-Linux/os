package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/openwall/yescrypt-go"
	"golang.org/x/term"

	"github.com/Construct-Linux/os/internal/account"
	"github.com/Construct-Linux/os/internal/securitykey"
	"github.com/Construct-Linux/os/internal/update"
)

// person is who the machine is installed for, and the password that unlocks it and logs them in.
type person struct {
	user     account.User
	password string
	lang     language
	// keys are the security keys that open /var beside the password: none, one, or one and the
	// backup kept somewhere safe.
	keys []securityKey
	// hostname is the name they give the machine, /etc/hostname; empty leaves os-release's.
	hostname string
}

// personFrom is the person the flags name, or, with neither, the one asked for at the terminal.
func personFrom(in *bufio.Reader, out io.Writer, name, realName, passwordFile string) (person, error) {
	if name != "" || passwordFile != "" {
		if name == "" || passwordFile == "" {
			return person{}, errors.New("--user and --password-file go together")
		}
		var pw string
		if passwordFile == "-" {
			// The graphical installer's pipe: a password never on a command line. Its first line:
			// the security keys' PINs may follow (--key-pin-file -).
			line, err := in.ReadString('\n')
			if err != nil && !(errors.Is(err, io.EOF) && line != "") {
				return person{}, err
			}
			pw = line
		} else {
			b, err := os.ReadFile(passwordFile)
			if err != nil {
				return person{}, err
			}
			pw = string(b)
		}
		if err := account.ValidName(name); err != nil {
			return person{}, err
		}
		if realName == "" {
			realName = name
		}
		pw = strings.TrimRight(pw, "\r\n")
		if pw == "" {
			return person{}, errors.New("the password is empty")
		}
		return newPerson(name, realName, pw), nil
	}
	return askPerson(in, out)
}

func newPerson(name, realName, password string) person {
	return person{
		user:     account.User{Name: name, RealName: realName, UID: account.FirstUID, Groups: account.Groups},
		password: password,
	}
}

// askPerson asks for a name, a login and a password typed twice, unechoed.
func askPerson(in *bufio.Reader, out io.Writer) (person, error) {
	fmt.Fprint(out, "\nWho is this machine for?\n\n  Full name: ")
	realName, err := readLine(in)
	if err != nil {
		return person{}, err
	}
	var name string
	for {
		suggested := suggestLogin(realName)
		fmt.Fprintf(out, "  Login [%s]: ", suggested)
		if name, err = readLine(in); err != nil {
			return person{}, err
		}
		if name == "" {
			name = suggested
		}
		if err := account.ValidName(name); err != nil {
			fmt.Fprintln(out, "  ", err)
			continue
		}
		break
	}
	if realName == "" {
		realName = name
	}
	fmt.Fprint(out, "\nOne password logs you in, and is what sudo and the lock screen ask for.\n\n")
	for {
		pw, err := readPassword(in, out, "  Password: ")
		if err != nil {
			return person{}, err
		}
		again, err := readPassword(in, out, "  Again:    ")
		if err != nil {
			return person{}, err
		}
		switch {
		case pw == "":
			fmt.Fprintln(out, "   a password is needed")
		case pw != again:
			fmt.Fprintln(out, "   they differ; once more")
		default:
			return newPerson(name, realName, pw), nil
		}
	}
}

// suggestLogin is a login made from a full name: its first word, lowercase, what ValidName takes.
func suggestLogin(realName string) string {
	first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(realName)), " ")
	var b strings.Builder
	for _, r := range first {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && b.Len() > 0) || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if s := b.String(); account.ValidName(s) == nil {
		return s
	}
	return "user"
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readPassword reads a line unechoed from a terminal, or as it comes from anything else.
func readPassword(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) && in.Buffered() == 0 {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		return string(b), err
	}
	return readLine(in)
}

// hashSetting is libxcrypt's default yescrypt cost, as crypt_gensalt(3) writes it for passwd and
// chpasswd: N=4096, r=32, p=1.
const hashSetting = "$y$j9T$"

// hashPassword is pw in crypt(3)'s yescrypt form, what libxcrypt - which pam_unix checks it with
// - makes for a new password: memory-hard, where SHA-512 crypt is a GPU's to try fast. Openwall's
// own Go implementation, with a salt of 16 random bytes as libxcrypt takes.
func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h, err := yescrypt.Hash([]byte(pw), []byte(hashSetting+crypt64(salt)))
	if err != nil {
		return "", fmt.Errorf("hashing the password: %w", err)
	}
	return string(h), nil
}

// crypt64 is b in crypt(3)'s base 64: its alphabet, least significant bits first, as yescrypt
// reads a salt.
func crypt64(b []byte) string {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var out []byte
	for i := 0; i < len(b); {
		value, bits := uint32(0), 0
		for ; bits < 24 && i < len(b); bits += 8 {
			value |= uint32(b[i]) << bits
			i++
		}
		for ; bits > 0; bits -= 6 {
			out = append(out, alphabet[value&0x3f])
			value >>= 6
		}
	}
	return string(out)
}

// prepareVar lays p on the new /var at dev: their record, GNOME logging them in, their home.
// What goes in /etc is written into the overlay's upper layer the initrd mounts /etc with.
func prepareVar(ctx context.Context, dev string, p person, keys []securitykey.Key) error {
	hash, err := hashPassword(p.password)
	if err != nil {
		return err
	}
	u := p.user
	u.HashedPassword = hash
	dir, unmount, err := mountFS(ctx, "ext4", "noatime", dev)
	if err != nil {
		return err
	}
	upper := filepath.Join(dir, "lib", "construct", "etc", "upper")
	err = account.Write(upper, factoryEtc, u)
	if err == nil {
		err = account.MakeHome(dir, u)
	}
	if err == nil {
		err = clearKeyring(filepath.Join(dir, "home", u.Name), u.UID)
	}
	// Logged in at boot: nothing of the person's opens without their password or their key,
	// which opened /var.
	if err == nil {
		err = account.Autologin(upper, u.Name)
	}
	// The security keys enrolled with the disk sign them in too.
	if err == nil && len(keys) > 0 {
		err = securitykey.Write(upper, keys)
	}
	// Their language and keyboard, which GDM, GNOME and the console start from, and the machine's
	// name.
	files := map[string][]byte{"locale.conf": p.lang.localeConf(), "vconsole.conf": p.lang.vconsole()}
	if p.hostname != "" {
		files["hostname"] = []byte(p.hostname + "\n")
	}
	for name, body := range files {
		if err == nil {
			err = os.WriteFile(filepath.Join(upper, name), body, 0o644)
		}
	}
	if uerr := unmount(); err == nil {
		err = uerr
	}
	return err
}

// factoryEtc holds the image's own account files (cmd/image's bake): an install starts the
// person's /etc from them.
const factoryEtc = "/usr/share/factory/etc"

// liveCmd gives a stick nobody is installed from the live user, logged in at boot
// (construct-live.service, before GDM), and the installer; a development root (the ext4 test
// disk) gets the live user too. An installed machine - a verified root that is no stick - has
// its person, written by construct install, or after a factory reset left it nobody, GNOME's
// Initial Setup makes one: it never gets the live user.
func liveCmd(etc, varDir string) error {
	stick := update.FromStick("/sys")
	if !stick && update.VerifiedRoot("/sys") {
		return nil
	}
	installed, err := account.HasPerson(etc)
	if err != nil || installed {
		return err
	}
	if err := account.Write(etc, factoryEtc, account.Live); err != nil {
		return fmt.Errorf("the live user: %w", err)
	}
	if err := account.Autologin(etc, account.Live.Name); err != nil {
		return err
	}
	if err := account.MakeHome(varDir, account.Live); err != nil {
		return err
	}
	// GNOME's first-login setup is the person's, on the machine they installed: the live session
	// has nothing to set up.
	home := filepath.Join(varDir, "home", account.Live.Name)
	if err := ownedFile(home, ".config/gnome-initial-setup-done", []byte("yes"), account.Live.UID); err != nil {
		return err
	}
	if err := clearKeyring(home, account.Live.UID); err != nil {
		return err
	}
	if !stick {
		return nil
	}
	return offerInstaller(home, installerEntry, account.Live.UID)
}

// clearKeyring gives the home a login keyring without a password, as GNOME Keyring keeps one in
// the clear: what protects it at rest is /var's encryption, opened at boot by the person's password
// or their security key. Either way GDM logs them in with the keyring open - after a security key
// no password was typed to unlock one with - and changing their password leaves nothing behind.
func clearKeyring(home string, uid int) error {
	for rel, body := range map[string]string{
		".local/share/keyrings/login.keyring": "[keyring]\ndisplay-name=Login\nctime=0\nmtime=0\nlock-on-idle=false\nlock-after=false\n",
		".local/share/keyrings/default":       "login\n",
	} {
		if err := ownedFile(home, rel, []byte(body), uid); err != nil {
			return err
		}
		// The secrets it comes to hold are its owner's alone, as GNOME Keyring makes them.
		if err := os.Chmod(filepath.Join(home, rel), 0o600); err != nil {
			return err
		}
	}
	return os.Chmod(filepath.Join(home, ".local/share/keyrings"), 0o700)
}

// ownedFile writes body to rel under home, each directory it makes on the way and the file
// itself uid's: a home's dot-directories are its owner's.
func ownedFile(home, rel string, body []byte, uid int) error {
	dir := home
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Lchown(dir, uid, uid); err != nil {
			return err
		}
	}
	path := filepath.Join(home, rel)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	return os.Lchown(path, uid, uid)
}

// installerEntry is the graphical installer's desktop entry, which only a stick's live session
// shows: an installed machine has no live user, and the test disk is no stick.
const installerEntry = "/usr/share/construct/io.github.construct_linux.Installer.desktop"

// offerInstaller puts the installer in home's applications and starts it at login, as uid's.
func offerInstaller(home, entry string, uid int) error {
	b, err := os.ReadFile(entry)
	if err != nil {
		return err
	}
	for _, dir := range []string{".local/share/applications", ".config/autostart"} {
		if err := ownedFile(home, filepath.Join(dir, filepath.Base(entry)), b, uid); err != nil {
			return err
		}
	}
	return nil
}
