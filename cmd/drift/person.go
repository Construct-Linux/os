package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/spin-stack/spindrift/internal/account"
)

// person is who the machine is installed for, and the password that unlocks it and logs them in.
type person struct {
	user     account.User
	password string
}

// personFrom is the person the flags name, or, with neither, the one asked for at the terminal.
func personFrom(in *bufio.Reader, out io.Writer, name, realName, passwordFile string) (person, error) {
	if name != "" || passwordFile != "" {
		if name == "" || passwordFile == "" {
			return person{}, errors.New("--user and --password-file go together")
		}
		b, err := os.ReadFile(passwordFile)
		if err != nil {
			return person{}, err
		}
		if err := account.ValidName(name); err != nil {
			return person{}, err
		}
		if realName == "" {
			realName = name
		}
		pw := strings.TrimRight(string(b), "\n")
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

// hashPassword is pw in crypt(3)'s SHA-512 form, which the image's glibc libcrypt checks (it has
// no yescrypt).
func hashPassword(ctx context.Context, pw string) (string, error) {
	cmd := exec.CommandContext(ctx, "openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(pw + "\n")
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hashing the password: %w", err)
	}
	h := strings.TrimSpace(string(b))
	if !strings.HasPrefix(h, "$6$") {
		return "", fmt.Errorf("hashing the password: openssl gave %q", h)
	}
	return h, nil
}

// prepareVar lays p on the new /var at dev: their record, GNOME logging them in, their home.
// What goes in /etc is written into the overlay's upper layer the initrd mounts /etc with.
func prepareVar(ctx context.Context, dev string, p person) error {
	hash, err := hashPassword(ctx, p.password)
	if err != nil {
		return err
	}
	u := p.user
	u.HashedPassword = hash
	dir, unmount, err := mountFS(ctx, "ext4", "noatime", dev)
	if err != nil {
		return err
	}
	upper := filepath.Join(dir, "lib", "spindrift", "etc", "upper")
	// No autologin: /var is not encrypted yet, so the password GDM asks for is all that stands
	// between whoever powers the machine on and the person's desktop.
	err = account.Write(upper, u)
	if err == nil {
		err = account.MakeHome(dir, u)
	}
	if uerr := unmount(); err == nil {
		err = uerr
	}
	return err
}

// liveCmd gives a root nobody is installed for - a live stick, the test disk - the live user,
// logged in at boot (spindrift-live.service, before GDM). An installed machine has its person,
// written by drift install, and never gets it.
func liveCmd(etc, varDir string) error {
	installed, err := account.HasPerson(etc)
	if err != nil || installed {
		return err
	}
	if err := account.Write(etc, account.Live); err != nil {
		return fmt.Errorf("the live user: %w", err)
	}
	if err := account.Autologin(etc, account.Live.Name); err != nil {
		return err
	}
	return account.MakeHome(varDir, account.Live)
}
