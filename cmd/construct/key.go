package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A security key does two things on an installed machine: it opens /var at boot with its PIN and a
// touch (a systemd-fido2 token in the LUKS2 header), and it signs the person in where sudo, polkit
// and the lock screen ask, with a touch (pam_u2f, etc/pam.d/construct-key). keysFile records each
// key's name and both of its credentials, so construct key can list a key and remove all of it;
// u2fMappings, which pam_u2f reads, is written from it in whole.
const (
	keysFile    = "construct/security-keys.json"
	u2fMappings = "u2f_mappings"
	// u2fOrigin is pam_u2f's origin and appid (etc/pam.d/construct-key): fixed, so renaming the
	// machine does not orphan its keys.
	u2fOrigin = "pam://construct"
)

// registeredKey is one security key on this machine.
type registeredKey struct {
	Name string `json:"name"`
	User string `json:"user"`
	// Slot is the LUKS2 keyslot it opens /var with, or -1 when /var is not encrypted (a stick).
	Slot int `json:"slot"`
	// U2F is its pam_u2f credential: key handle, public key, COSE type and options.
	U2F   string `json:"u2f"`
	Added string `json:"added"`
}

// readKeys reads the keys registered under etc: none when nothing was ever registered.
func readKeys(etc string) ([]registeredKey, error) {
	b, err := os.ReadFile(filepath.Join(etc, keysFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Keys []registeredKey `json:"keys"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", keysFile, err)
	}
	return f.Keys, nil
}

// writeKeys writes the record and pam_u2f's mappings from it, each whole or not at all.
func writeKeys(etc string, keys []registeredKey) error {
	b, err := json.MarshalIndent(struct {
		Keys []registeredKey `json:"keys"`
	}{keys}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(etc, filepath.Dir(keysFile)), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(etc, keysFile), append(b, '\n')); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(etc, u2fMappings), []byte(mappingsOf(keys)))
}

// mappingsOf is pam_u2f's central file: a line an account, its credentials after its name.
func mappingsOf(keys []registeredKey) string {
	byUser := map[string][]string{}
	for _, k := range keys {
		if k.U2F != "" {
			byUser[k.User] = append(byUser[k.User], k.U2F)
		}
	}
	users := make([]string, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	sort.Strings(users)
	var b strings.Builder
	for _, u := range users {
		b.WriteString(u + ":" + strings.Join(byUser[u], ":") + "\n")
	}
	return b.String()
}

// writeAtomic replaces path with body: the reader sees the old file or the new one, never half.
func writeAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// u2fCredential makes a pam_u2f credential for name on the key at device: fido2-cred makes it, on
// that key and no other - pamu2fcfg takes the first key it finds, and a person adding a backup may
// have both plugged in - and verifies it, and the line is pamu2fcfg's. The key asks for a touch,
// and for its PIN when it makes no credential without one.
func u2fCredential(ctx context.Context, device, name, pin string) (string, error) {
	dir, err := os.MkdirTemp("/run", "construct-u2f-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cdh, uid := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(cdh); err != nil {
		return "", err
	}
	if _, err := rand.Read(uid); err != nil {
		return "", err
	}
	in, made, verified := filepath.Join(dir, "in"), filepath.Join(dir, "made"), filepath.Join(dir, "verified")
	b64 := base64.StdEncoding.EncodeToString
	if err := os.WriteFile(in, []byte(b64(cdh)+"\n"+u2fOrigin+"\n"+name+"\n"+b64(uid)+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := withPIN(ctx, pin, "fido2-cred", "-M", "-i", in, "-o", made, device); err != nil {
		return "", err
	}
	if err := cmd(ctx, nil, nil, "fido2-cred", "-V", "-i", made, "-o", verified); err != nil {
		return "", err
	}
	out, err := os.ReadFile(verified)
	if err != nil {
		return "", err
	}
	return u2fLine(out)
}

// u2fLine turns fido2-cred -V's output - the credential's id in base64, then its public key in
// PEM - into pam_u2f's form: the id, the raw P-256 point x||y in base64, es256, and +presence (a
// touch, no PIN: pam_u2f's own default).
func u2fLine(verified []byte) (string, error) {
	id, rest, ok := bytes.Cut(verified, []byte("\n"))
	if !ok || len(bytes.TrimSpace(id)) == 0 {
		return "", errors.New("fido2-cred wrote no credential id")
	}
	if _, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(id))); err != nil {
		return "", fmt.Errorf("the credential id: %w", err)
	}
	block, _ := pem.Decode(rest)
	if block == nil {
		return "", errors.New("fido2-cred wrote no public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("a %T key: pam_u2f is given an es256 one", pub)
	}
	point, err := ec.Bytes()
	if err != nil {
		return "", err
	}
	// Bytes is the uncompressed point, 0x04||x||y; pam_u2f's key is x||y, as pamu2fcfg writes it.
	if len(point) != 65 || point[0] != 4 {
		return "", fmt.Errorf("a %d-byte point: es256's is 65", len(point))
	}
	return string(bytes.TrimSpace(id)) + "," + base64.StdEncoding.EncodeToString(point[1:]) + ",es256,+presence", nil
}

// withPIN runs a libfido2 tool that may ask for the key's PIN: they read it from a terminal and
// drop what was typed before they asked, so the tool gets one (script) and the PIN is typed when
// the question appears - never an argument, which another process could read.
func withPIN(ctx context.Context, pin, name string, args ...string) error {
	quoted := []string{name}
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	c := exec.CommandContext(ctx, "script", "-qec", strings.Join(quoted, " "), "/dev/null")
	stdin, err := c.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	var said bytes.Buffer
	buf := make([]byte, 512)
	answered := 0
	for {
		n, rerr := stdout.Read(buf)
		said.Write(buf[:n])
		// Each question about the PIN gets it once: setting a new one asks twice.
		for ; answered < bytes.Count(said.Bytes(), []byte("PIN")); answered++ {
			if pin == "" {
				_ = c.Process.Kill()
				_ = c.Wait()
				return fmt.Errorf("%s: the key asks for its PIN, and none was given", name)
			}
			_, _ = io.WriteString(stdin, pin+"\n")
		}
		if rerr != nil {
			break
		}
	}
	_ = stdin.Close()
	if err := c.Wait(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(strings.ReplaceAll(said.String(), pin, "")))
	}
	return nil
}

// fido2Slots are the LUKS2 keyslots of partition that a systemd-fido2 token opens.
func fido2Slots(ctx context.Context, partition string) ([]int, error) {
	var out bytes.Buffer
	if err := cmdOut(ctx, nil, nil, &out, "cryptsetup", "luksDump", "--dump-json-metadata", partition); err != nil {
		return nil, err
	}
	return fido2SlotsOf(out.Bytes())
}

// fido2SlotsOf reads them from the header's JSON.
func fido2SlotsOf(header []byte) ([]int, error) {
	var h struct {
		Tokens map[string]struct {
			Type     string   `json:"type"`
			Keyslots []string `json:"keyslots"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(header, &h); err != nil {
		return nil, err
	}
	var slots []int
	for _, t := range h.Tokens {
		if t.Type != "systemd-fido2" {
			continue
		}
		for _, s := range t.Keyslots {
			n, err := strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("keyslot %q: %w", s, err)
			}
			slots = append(slots, n)
		}
	}
	slices.Sort(slots)
	return slots, nil
}

// newSlot is the one slot in after that was not in before.
func newSlot(before, after []int) (int, error) {
	var added []int
	for _, s := range after {
		if !slices.Contains(before, s) {
			added = append(added, s)
		}
	}
	if len(added) != 1 {
		return 0, fmt.Errorf("%d new security key slots, want 1", len(added))
	}
	return added[0], nil
}

// varPartition is the encrypted partition the running /var is opened from, or "" when /var is not
// encrypted (a stick).
func varPartition(ctx context.Context) (string, error) {
	if _, err := os.Stat("/dev/mapper/var"); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	var out bytes.Buffer
	if err := cmdOut(ctx, nil, nil, &out, "cryptsetup", "status", "var"); err != nil {
		return "", err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "device" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("cryptsetup status var names no device")
}

// keyCmd is construct key: the security keys registered on this machine, adding one and removing
// one. Adding and removing ask for the person's password on standard input: it unlocks the LUKS2
// header for the key's slot.
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
	keys, err := readKeys("/etc")
	if err != nil {
		return err
	}
	if *asJSON {
		// Encrypted says whether adding a key asks for the password: it unlocks the disk's header.
		partition, err := varPartition(ctx)
		if err != nil {
			return err
		}
		if keys == nil {
			keys = []registeredKey{}
		}
		return json.NewEncoder(stdout).Encode(struct {
			Encrypted bool            `json:"encrypted"`
			Keys      []registeredKey `json:"keys"`
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
	in := bufio.NewReader(stdin)
	pin := ""
	if *pinFile != "" && *pinFile != "-" {
		b, err := os.ReadFile(*pinFile)
		if err != nil {
			return err
		}
		pin = strings.TrimRight(string(b), "\r\n")
	}
	keys, err := readKeys("/etc")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(keys, func(k registeredKey) bool { return k.Name == *name }) {
		return fmt.Errorf("key add: a key is called %q already", *name)
	}
	k := registeredKey{Name: *name, User: owner, Slot: -1, Added: time.Now().UTC().Format(time.DateOnly)}
	partition, err := varPartition(ctx)
	if err != nil {
		return err
	}
	if partition != "" {
		password, err := keyPassword(in)
		if err != nil {
			return err
		}
		if *pinFile == "-" {
			if pin, err = readLine(in); err != nil {
				return fmt.Errorf("key add: the PIN, after the password: %w", err)
			}
		}
		if pin == "" {
			return errors.New("key add: the disk opens with the key's PIN and a touch: --pin-file")
		}
		fmt.Fprintln(stdout, "touch the key: it will open the disk")
		before, err := fido2Slots(ctx, partition)
		if err != nil {
			return err
		}
		if err := enrollSecurityKey(ctx, partition, password, securityKey{device: dev, pin: pin}); err != nil {
			return err
		}
		after, err := fido2Slots(ctx, partition)
		if err != nil {
			return err
		}
		if k.Slot, err = newSlot(before, after); err != nil {
			return err
		}
	}
	// No disk to unlock, no password: the PIN, if any, is the first line.
	if partition == "" && *pinFile == "-" {
		if pin, err = readLine(in); err != nil {
			return fmt.Errorf("key add: the PIN, on standard input: %w", err)
		}
	}
	fmt.Fprintln(stdout, "touch the key again: it will sign you in")
	if k.U2F, err = u2fCredential(ctx, dev, owner, pin); err != nil {
		return err
	}
	if err := writeKeys("/etc", append(keys, k)); err != nil {
		return err
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
	keys, err := readKeys("/etc")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k registeredKey) bool { return k.Name == *name })
	if i < 0 {
		return fmt.Errorf("key remove: no key is called %q", *name)
	}
	k := keys[i]
	if k.Slot >= 0 {
		partition, err := varPartition(ctx)
		if err != nil {
			return err
		}
		slots, err := fido2Slots(ctx, partition)
		if err != nil {
			return err
		}
		// Only a slot a security key's token opens: the record never wipes the password's.
		if slices.Contains(slots, k.Slot) {
			password, err := keyPassword(bufio.NewReader(stdin))
			if err != nil {
				return err
			}
			if err := cmd(ctx, nil, []string{"PASSWORD=" + password}, "systemd-cryptenroll",
				"--wipe-slot="+strconv.Itoa(k.Slot), partition); err != nil {
				return err
			}
		}
	}
	if err := writeKeys("/etc", slices.Delete(keys, i, i+1)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s\n", k.Name)
	return nil
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
	var out bytes.Buffer
	if err := cmdOut(ctx, nil, nil, &out, "fido2-token", "-L"); err != nil {
		return "", err
	}
	var devs []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if dev, _, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(dev, "/dev/") {
			devs = append(devs, dev)
		}
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

// lockOnRemoval, when it exists, asks for the sessions to lock as a security key is unplugged:
// the person walks away with the key, and the screen locks behind them. Off unless they turn it on.
const lockOnRemoval = "construct/lock-on-key-removal"

// keyLockOnRemoval is construct key lock-on-removal [on|off]: says, or sets, whether unplugging a
// key locks the screen.
func keyLockOnRemoval(args []string, stdout io.Writer, etc string) error {
	path := filepath.Join(etc, lockOnRemoval)
	switch {
	case len(args) == 0:
		_, err := os.Stat(path)
		on := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Fprintln(stdout, map[bool]string{true: "on", false: "off"}[on])
		return nil
	case len(args) == 1 && args[0] == "on":
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return writeAtomic(path, nil)
	case len(args) == 1 && args[0] == "off":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return errors.New("key lock-on-removal: on, off, or nothing to say which")
}

// keyRemoved is what udev runs as a security key is unplugged (usr/lib/udev/rules.d): the
// sessions lock, if the person asked for it.
func keyRemoved(ctx context.Context, etc string) error {
	if _, err := os.Stat(filepath.Join(etc, lockOnRemoval)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return cmd(ctx, nil, nil, "loginctl", "lock-sessions")
}
