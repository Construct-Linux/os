package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Construct-Linux/os/internal/qemu"
)

// testKey is the harness's SSH key, made once and kept under .cache: the guest receives the
// public half as a credential at every boot.
func testKey() (ssh.Signer, string, error) {
	file := filepath.Join(".cache", "test_ed25519")
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, "", gerr
		}
		block, merr := ssh.MarshalPrivateKey(priv, "construct test")
		if merr != nil {
			return nil, "", merr
		}
		data = pem.EncodeToMemory(block)
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, "", err
	}
	return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// guest runs commands in the VM as root through systemd-ssh-proxy over vsock.
type guest struct {
	cid int
}

func (g *guest) run(script string, stdin string) (string, error) {
	var out string
	var err error
	for try := 1; ; try++ {
		var stderr string
		out, stderr, err = g.ssh(script, stdin)
		// A connection that never reached the guest's shell ran nothing, and is tried again: a
		// busy guest's sshd can take longer than ConnectTimeout to send its banner - on the release
		// builder one did, during the type matrix, and failed a release on a check that never ran.
		if err == nil || !notConnected(stderr) || try == connectTries {
			if err != nil {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
			}
			return out, err
		}
		time.Sleep(time.Duration(try) * 5 * time.Second)
	}
}

// connectTries is how many times run connects before a guest that does not answer is a failure.
const connectTries = 3

// answers is nil when the guest's sshd answers now, asked once: what waits for a guest to come up
// or go down loops on it, and a retry inside would only blur when it did.
func (g *guest) answers() error {
	_, stderr, err := g.ssh("true", "")
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (g *guest) ssh(script, stdin string) (string, string, error) {
	// ServerAlive: a guest that stops answering mid-command - a kernel that froze, a QEMU that
	// stopped - ends the command in a minute instead of hanging the run, which on the release
	// builder is the two hours it has. A command that runs long in a guest that answers goes on:
	// the distrobox, the install and the bench do, and a command that may hang has its own timeout.
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
		"-o", "IdentitiesOnly=yes", "-i", filepath.Join(".cache", "test_ed25519"),
		fmt.Sprintf("root@vsock/%d", g.cid), script)
	cmd.Stdin = strings.NewReader(stdin)
	var outb, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outb, &errb
	err := cmd.Run()
	return outb.String(), errb.String(), err
}

// notConnected says ssh failed before the guest ran anything: no banner, no connection. A command
// that ran and failed, or a connection that dropped while it ran, is not this.
func notConnected(stderr string) bool {
	return strings.Contains(stderr, "during banner exchange") || strings.Contains(stderr, "Connection refused") ||
		strings.Contains(stderr, "connect to host") && strings.Contains(stderr, "timed out")
}

// bootGuest starts m and waits up to d for its sshd to answer, stopping it when it does not -
// with the screen it showed then in _output/no-answer.png, since nothing else says how far it got.
func bootGuest(m qemu.Machine, d time.Duration) (*qemu.VM, *guest, error) {
	v, err := m.Start()
	if err != nil {
		return nil, nil, err
	}
	g, err := answer(v, d)
	if err != nil {
		return nil, nil, err
	}
	return v, g, nil
}

// answer waits up to d for v's sshd, as bootGuest does for a machine it started.
func answer(v *qemu.VM, d time.Duration) (*guest, error) {
	g := &guest{cid: v.CID}
	if err := g.wait(d); err != nil {
		shot := filepath.Join(out, "no-answer.png")
		_ = v.PNG(shot)
		_ = v.Stop()
		return nil, fmt.Errorf("the guest never answered over vsock (its screen: %s): %w", shot, err)
	}
	return g, nil
}

func (g *guest) wait(d time.Duration) error {
	var err error
	until(d, 3*time.Second, func() bool { err = g.answers(); return err == nil })
	return err
}

// until asks cond every tick until it answers true, or d has passed, and says whether it did.
// What cond read last is its closure's to keep; a d of 0 asks once.
func until(d, every time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(every) {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// evalScript calls org.gnome.Shell.Eval in the test user's session with the JavaScript on stdin,
// so nothing in it needs quoting for a shell.
const evalScript = `js=$(cat); exec ` + asLive +
	`gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.gnome.Shell.Eval "$js"`

// eval runs JavaScript in GNOME Shell and returns its JSON-encoded result. The script travels
// base64-encoded and is decoded in the shell: gdbus reads its argument as GVariant text, which
// takes one level of backslashes off ('\\u' arrived as a malformed '\u', '\\n' as a line
// break inside a string).
func (g *guest) eval(js string) (string, error) {
	prelude := `const Shell = imports.gi.Shell; const GLib = imports.gi.GLib; `
	script := prelude + js
	wrapped := `const GLib = imports.gi.GLib; eval(new TextDecoder().decode(GLib.base64_decode('` +
		base64.StdEncoding.EncodeToString([]byte(script)) + `')))`
	outs, err := g.run(evalScript, wrapped)
	if err != nil {
		return "", err
	}
	// gdbus prints (true, '"value"') or (false, 'error').
	outs = strings.TrimSpace(outs)
	ok := strings.HasPrefix(outs, "(true, ")
	i, j := strings.Index(outs, "'"), strings.LastIndex(outs, "'")
	val := ""
	if i >= 0 && j > i {
		// gdbus prints the string as GVariant text, which escapes backslashes and quotes.
		val = strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(outs[i+1 : j])
	}
	if !ok {
		return val, fmt.Errorf("eval: %s", outs)
	}
	return val, nil
}

// waitShell waits up to d for GNOME Shell to finish starting up.
func (g *guest) waitShell(d time.Duration) error {
	var err error
	var v string
	if until(d, 3*time.Second, func() bool {
		v, err = g.eval(`Main.layoutManager._startingUp === false`)
		return err == nil && v == "true"
	}) {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("still starting up (%s)", v)
	}
	return err
}

// windows is how many windows the app id has open, as the shell sees it: 0 for one not installed,
// -1 when the shell does not answer.
func (g *guest) windows(id string) int {
	var n int
	if err := g.evalJSON(fmt.Sprintf(`return Shell.AppSystem.get_default().lookup_app(%q)?.get_windows().length ?? 0;`, id), &n); err != nil {
		return -1
	}
	return n
}

// quitApp asks an app to quit, as closing its last window does; one not running is no error.
// quitApp asks the app to quit and waits for its windows to go, asking again every few seconds,
// up to a limit: a window left open covers every view after it (a Firefox that opened its page
// late, after the quit, was the desk of every light view). It answers whether the app is gone.
func (g *guest) quitApp(id string) bool {
	windows := func() int {
		var n int
		if err := g.evalJSON(fmt.Sprintf(`return Shell.AppSystem.get_default().lookup_app(%q)?.get_windows().length ?? 0;`, id), &n); err != nil {
			return -1
		}
		return n
	}
	for range 4 {
		_, _ = g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q)?.request_quit(); true`, id))
		if until(5*time.Second, 500*time.Millisecond, func() bool { return windows() == 0 }) {
			return true
		}
	}
	return false
}

// version is the IMAGE_VERSION the guest booted, or "" when it does not answer.
func (g *guest) version() string {
	out, err := g.run(`. /usr/lib/os-release && echo $IMAGE_VERSION`, "")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (g *guest) uptimeOver(d time.Duration) bool {
	s, err := g.uptime()
	return err == nil && s > d.Seconds()
}

// reboot asks the guest to restart and waits for it to stop answering.
func (g *guest) reboot() {
	_, _ = g.run(`systemctl reboot`, "")
	g.waitGone(2 * time.Minute)
}

// waitGone waits up to d for the guest to stop answering: a reboot, a suspend or a reset it was
// asked for has begun.
func (g *guest) waitGone(d time.Duration) {
	until(d, time.Second, func() bool { return g.answers() != nil })
}

// waitUnit2Active waits for unit to be active, and fails as soon as it failed.
func (g *guest) waitUnit2Active(unit string, d time.Duration) (bool, string) {
	state := ""
	until(d, 3*time.Second, func() bool {
		out, _ := g.run("systemctl is-active "+unit, "")
		state = strings.TrimSpace(out)
		return state == "active" || state == "failed"
	})
	return state == "active", unit + ": " + state
}

// runIn boots the disk as test does, runs script in it as root once sshd answers, prints what it
// wrote, and stops the machine: a look inside a boot without a scenario for it.
func runIn(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	stick := fl.Bool("usb", false, "boot the USB disk instead of the ext4 test disk")
	wait := fl.Duration("wait", 0, "how long to wait after sshd answers, for the boot to settle")
	nvme := fl.String("nvme", "", "with -vars: boot this NVMe disk image (the lifecycle's), with -sticks plugged in")
	vars := fl.String("vars", "", "the firmware's variable store to boot with")
	sticks := fl.String("sticks", "", "comma-separated stick images to plug in")
	tpm := fl.String("tpm", "", "with -vars: the swtpm state directory to give the machine (the lifecycle's)")
	key := fl.String("key", "", "plug in a FIDO2 security key, QEMU's CanoKey, its state in this file (made when missing)")
	poweroff := fl.Bool("poweroff", false, "end with systemctl poweroff, as a person does, instead of cutting the power: the disks afterwards are what a shutdown left")
	var creds appendFlag
	fl.Var(&creds, "cred", "a systemd credential for the boot, name=value; repeatable")
	var extra appendFlag
	fl.Var(&extra, "append", "with -vars or -usb: a kernel option for this boot, which systemd-stub takes from the firmware without Secure Boot (systemd.log_level=debug); repeatable")
	_ = fl.Parse(args)
	// The ext4 test disk's kernel is given to QEMU directly, with no systemd-stub to read an
	// option from the firmware: it would be dropped without a word.
	if len(extra) > 0 && *vars == "" && !*stick {
		return errors.New("run: -append needs a boot through the firmware: -vars or -usb")
	}
	_, pub, err := testKey()
	if err != nil {
		return err
	}
	m := qemu.Machine{Memory: vmMemory, PubKey: pub, Vars: *vars, NVMe: *nvme, TPM: *tpm, Dir: out}
	if *vars == "" {
		if m, err = testMachine(vmMemory, pub, *stick); err != nil {
			return err
		}
	} else if *sticks != "" {
		m.Sticks = strings.Split(*sticks, ",")
	}
	m.Credentials, m.Cmdline, m.SecurityKey = creds, extra, *key
	v, g, err := bootGuest(m, 4*time.Minute)
	if err != nil {
		return err
	}
	time.Sleep(*wait)
	outs, err := g.run(strings.Join(fl.Args(), " "), "")
	fmt.Print(outs)
	if *poweroff {
		powerOff(v, g)
	} else {
		_ = v.Stop()
	}
	return err
}

// uptime is the guest's seconds since boot, the clock journalctl -o short-monotonic stamps lines
// with.
func (g *guest) uptime() (float64, error) {
	out, err := g.run(`cut -d' ' -f1 /proc/uptime`, "")
	if err != nil {
		return 0, err
	}
	var s float64
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%f", &s); err != nil {
		return 0, fmt.Errorf("/proc/uptime: %q: %w", out, err)
	}
	return s, nil
}
