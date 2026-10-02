package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
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
		block, merr := ssh.MarshalPrivateKey(priv, "spindrift test")
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
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "IdentitiesOnly=yes", "-i", filepath.Join(".cache", "test_ed25519"),
		fmt.Sprintf("root@vsock/%d", g.cid), script)
	cmd.Stdin = strings.NewReader(stdin)
	var outb, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outb, &errb
	err := cmd.Run()
	if err != nil {
		return outb.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return outb.String(), nil
}

// bootGuest starts m and waits up to d for its sshd to answer, stopping it when it does not -
// with the screen it showed then in _output/no-answer.png, since nothing else says how far it got.
func (m machine) bootGuest(d time.Duration) (*vm, *guest, error) {
	v, err := m.start()
	if err != nil {
		return nil, nil, err
	}
	g := &guest{cid: v.cid}
	if err := g.wait(d); err != nil {
		shot := filepath.Join(out, "no-answer.ppm")
		if v.screenshot(shot) == nil {
			time.Sleep(500 * time.Millisecond)
			_ = ppmToPNG(shot, strings.TrimSuffix(shot, ".ppm")+".png")
		}
		v.stop()
		return nil, nil, fmt.Errorf("the guest never answered over vsock (its screen: %s.png): %w", strings.TrimSuffix(shot, ".ppm"), err)
	}
	return v, g, nil
}

func (g *guest) wait(d time.Duration) error {
	deadline := time.Now().Add(d)
	var err error
	for time.Now().Before(deadline) {
		if _, err = g.run("true", ""); err == nil {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return err
}

// evalScript calls org.gnome.Shell.Eval in the test user's session with the JavaScript on stdin,
// so nothing in it needs quoting for a shell.
const evalScript = `js=$(cat); exec sudo -u live env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus ` +
	`gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.gnome.Shell.Eval "$js"`

// eval runs JavaScript in GNOME Shell and returns its JSON-encoded result.
func (g *guest) eval(js string) (string, error) {
	prelude := `const Shell = imports.gi.Shell; const GLib = imports.gi.GLib; `
	outs, err := g.run(evalScript, prelude+js)
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

func (g *guest) waitShell(d time.Duration) (string, error) {
	deadline := time.Now().Add(d)
	var err error
	var v string
	for time.Now().Before(deadline) {
		if v, err = g.eval(`Main.layoutManager._startingUp === false`); err == nil && v == "true" {
			return v, nil
		}
		time.Sleep(3 * time.Second)
	}
	if err == nil {
		err = fmt.Errorf("still starting up (%s)", v)
	}
	return v, err
}

// waitUnit waits for a oneshot unit to run, or to be skipped by its condition, and reports how
// it ended. A unit that has not started yet is also inactive with Result=success, so it counts as
// done only once its main process or its condition has a timestamp.
func (g *guest) waitUnit(unit string, d time.Duration) (bool, string) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out, err := g.run("systemctl show -p ActiveState -p Result -p ExecMainExitTimestampMonotonic -p ConditionResult -p ConditionTimestampMonotonic "+unit, "")
		p := map[string]string{}
		for _, l := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				p[k] = v
			}
		}
		if err == nil && p["ActiveState"] != "" && p["ActiveState"] != "activating" {
			ran := p["ExecMainExitTimestampMonotonic"] != "0"
			skipped := p["ConditionResult"] == "no" && p["ConditionTimestampMonotonic"] != "0"
			if ran || skipped {
				return p["Result"] == "success", fmt.Sprintf("%s: %s/%s (skipped: %v)", unit, p["ActiveState"], p["Result"], skipped)
			}
		}
		time.Sleep(5 * time.Second)
	}
	return false, fmt.Sprintf("%s did not finish within %v", unit, d)
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
	out, err := g.run(`cut -d. -f1 /proc/uptime`, "")
	if err != nil {
		return false
	}
	var s int
	_, _ = fmt.Sscan(strings.TrimSpace(out), &s)
	return time.Duration(s)*time.Second > d
}

// reboot asks the guest to restart and waits for it to stop answering.
func (g *guest) reboot() {
	_, _ = g.run(`systemctl reboot`, "")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := g.run("true", ""); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
}

// waitUnit2Active waits for unit to be active, and fails as soon as it failed.
func (g *guest) waitUnit2Active(unit string, d time.Duration) (bool, string) {
	deadline := time.Now().Add(d)
	state := ""
	for time.Now().Before(deadline) {
		out, _ := g.run("systemctl is-active "+unit, "")
		state = strings.TrimSpace(out)
		if state == "active" || state == "failed" {
			break
		}
		time.Sleep(3 * time.Second)
	}
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
	_ = fl.Parse(args)
	_, pub, err := testKey()
	if err != nil {
		return err
	}
	m := machine{mem: "6G", pubkey: pub, vars: *vars, nvme: *nvme}
	if *vars == "" {
		if m, err = testMachine("6G", pub, *stick); err != nil {
			return err
		}
	} else if *sticks != "" {
		m.sticks = strings.Split(*sticks, ",")
	}
	v, g, err := m.bootGuest(4 * time.Minute)
	if err != nil {
		return err
	}
	defer v.stop()
	time.Sleep(*wait)
	outs, err := g.run(strings.Join(fl.Args(), " "), "")
	fmt.Print(outs)
	return err
}
