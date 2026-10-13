package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/securitykey"
)

// keyCmd is construct key: the security keys on this machine (internal/securitykey), adding one,
// removing one, and whether unplugging one locks the screen. Adding and removing ask for the
// person's password on standard input: it unlocks the LUKS2 header for the key's slot.
func keyCmd(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("key: list, add or remove")
	}
	switch args[0] {
	case "list":
		return keyList(ctx, args[1:], stdout)
	case "add":
		return keyAdd(ctx, args[1:], stdin, stdout)
	case "remove":
		return keyRemove(ctx, args[1:], stdin, stdout)
	case "lock-on-removal":
		return keyLockOnRemoval(args[1:], stdout, "/etc")
	case "removed":
		return keyRemoved(ctx, "/etc")
	}
	return fmt.Errorf("key: %q is none of list, add, remove, lock-on-removal, removed", args[0])
}

func keyList(ctx context.Context, args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("key list", flag.ContinueOnError)
	asJSON := fl.Bool("json", false, "the keys as JSON")
	if err := fl.Parse(args); err != nil {
		return err
	}
	keys, err := securitykey.Read("/etc")
	if err != nil {
		return err
	}
	if *asJSON {
		// Encrypted says whether adding a key asks for the password: it unlocks the disk's header.
		partition, err := securitykey.VarPartition(ctx)
		if err != nil {
			return err
		}
		if keys == nil {
			keys = []securitykey.Key{}
		}
		return json.NewEncoder(stdout).Encode(struct {
			Encrypted bool              `json:"encrypted"`
			Keys      []securitykey.Key `json:"keys"`
		}{partition != "", keys})
	}
	for _, k := range keys {
		disk := "opens the disk"
		if k.Slot < 0 {
			disk = "signs in only"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\tadded %s\n", k.Name, k.User, disk, k.Added)
	}
	return nil
}

func keyAdd(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fl := flag.NewFlagSet("key add", flag.ContinueOnError)
	device := fl.String("device", "", "the key's hidraw node; the one key plugged in when unset")
	name := fl.String("name", "", "what to call it: \"YubiKey\", \"Backup\"")
	who := fl.String("user", "", "the account it signs in; the one pkexec ran this for when unset")
	pinFile := fl.String("pin-file", "", "the key's PIN: a file, or - for the line after the password on standard input (pkexec passes no other descriptor)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("key add: --name names the key")
	}
	owner, err := keyUser(*who)
	if err != nil {
		return err
	}
	dev := *device
	if dev == "" {
		if dev, err = onlyKey(ctx); err != nil {
			return err
		}
	}
	keys, err := securitykey.Read("/etc")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(keys, func(k securitykey.Key) bool { return k.Name == *name }) {
		return fmt.Errorf("key add: a key is called %q already", *name)
	}
	partition, err := securitykey.VarPartition(ctx)
	if err != nil {
		return err
	}
	// What standard input carries: the password when the disk is encrypted, then the PIN for -.
	in := bufio.NewReader(stdin)
	password := ""
	if partition != "" {
		if password, err = keyPassword(in); err != nil {
			return err
		}
	}
	pin, err := keyPIN(*pinFile, in)
	if err != nil {
		return err
	}
	k := securitykey.Key{Name: *name, User: owner, Slot: -1, Added: time.Now().UTC().Format(time.DateOnly)}
	if partition != "" {
		if pin == "" {
			return errors.New("key add: the disk opens with the key's PIN and a touch: --pin-file")
		}
		err = untilTouched(func(try int) (err error) {
			fmt.Fprintln(stdout, touchPrompt(try, "touch the key: it will open the disk"))
			k.Slot, err = securitykey.Enroll(ctx, partition, password, dev, pin)
			return err
		})
		if err != nil {
			return err
		}
	}
	// From here a failure takes the slot back: a slot with no record is a key with no name, which
	// nothing lists and construct key remove cannot reach.
	fail := func(err error) error {
		if k.Slot >= 0 {
			if werr := securitykey.Wipe(ctx, partition, password, k.Slot); werr != nil {
				return fmt.Errorf("%w; and its slot %d, which stays in the disk's header: %v", err, k.Slot, werr)
			}
		}
		return err
	}
	err = untilTouched(func(try int) (err error) {
		fmt.Fprintln(stdout, touchPrompt(try, "touch the key again: it will sign you in"))
		k.U2F, err = securitykey.Credential(ctx, dev, owner, pin)
		return err
	})
	if err != nil {
		return fail(err)
	}
	if err := securitykey.Write("/etc", append(keys, k)); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "added %s\n", k.Name)
	return nil
}

func keyRemove(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fl := flag.NewFlagSet("key remove", flag.ContinueOnError)
	name := fl.String("name", "", "the key to remove, as construct key list names it")
	if err := fl.Parse(args); err != nil {
		return err
	}
	keys, err := securitykey.Read("/etc")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k securitykey.Key) bool { return k.Name == *name })
	if i < 0 {
		return fmt.Errorf("key remove: no key is called %q", *name)
	}
	k := keys[i]
	if k.Slot >= 0 {
		partition, err := securitykey.VarPartition(ctx)
		if err != nil {
			return err
		}
		password, err := keyPassword(bufio.NewReader(stdin))
		if err != nil {
			return err
		}
		if err := securitykey.Wipe(ctx, partition, password, k.Slot); err != nil {
			return err
		}
	}
	if err := securitykey.Write("/etc", slices.Delete(keys, i, i+1)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s\n", k.Name)
	return nil
}

// keyLockOnRemoval is construct key lock-on-removal [on|off]: says, or sets, whether unplugging a
// key locks the screen.
func keyLockOnRemoval(args []string, stdout io.Writer, etc string) error {
	switch {
	case len(args) == 0:
		on, err := securitykey.LocksOnRemoval(etc)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, map[bool]string{true: "on", false: "off"}[on])
		return nil
	case len(args) == 1 && (args[0] == "on" || args[0] == "off"):
		return securitykey.SetLockOnRemoval(etc, args[0] == "on")
	}
	return errors.New("key lock-on-removal: on, off, or nothing to say which")
}

// keyRemoved is what udev runs as a security key is unplugged (usr/lib/udev/rules.d): the
// sessions lock, if the person asked for it.
func keyRemoved(ctx context.Context, etc string) error {
	on, err := securitykey.LocksOnRemoval(etc)
	if err != nil || !on {
		return err
	}
	return cmd(ctx, nil, nil, "loginctl", "lock-sessions")
}

// keyUser is the account a key signs in: the one named, or the one pkexec ran construct for.
func keyUser(name string) (string, error) {
	if name == "" {
		uid := os.Getenv("PKEXEC_UID")
		if uid == "" {
			return "", errors.New("key add: --user names the account (or run it through pkexec)")
		}
		u, err := user.LookupId(uid)
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	if _, err := user.Lookup(name); err != nil {
		return "", err
	}
	return name, nil
}

// onlyKey is the hidraw node of the one FIDO2 key plugged in.
func onlyKey(ctx context.Context) (string, error) {
	devs, err := securitykey.Devices(ctx)
	if err != nil {
		return "", err
	}
	if len(devs) != 1 {
		return "", fmt.Errorf("%d security keys plugged in: plug in the one to add, or name it with --device", len(devs))
	}
	return devs[0], nil
}

// keyPassword is the person's password, which unlocks the LUKS2 header for a key's slot: typed
// at a terminal, or the first line of what stdin carries (pkexec from the app).
func keyPassword(in *bufio.Reader) (string, error) {
	pw, err := readPassword(in, os.Stderr, "Password: ")
	if err == nil && pw == "" {
		err = errors.New("the password, on standard input")
	}
	return pw, err
}

// keyPIN is the key's PIN from --pin-file: a file, the next line of in for -, or none.
func keyPIN(file string, in *bufio.Reader) (string, error) {
	switch file {
	case "":
		return "", nil
	case "-":
		pin, err := readSecret(in)
		if err != nil {
			return "", fmt.Errorf("key add: the PIN, on standard input: %w", err)
		}
		return pin, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
