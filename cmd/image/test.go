package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// app is one scenario: the desktop file GNOME Shell launches, and how long its first window may
// take to appear.
type app struct {
	id      string
	timeout time.Duration
}

// apps is what "usable" means, as a list the harness checks: each must be installed, open a
// window, and leave no core dump behind.
var apps = []app{
	{"org.gnome.Ptyxis.desktop", 20 * time.Second},
	{"org.gnome.Settings.desktop", 30 * time.Second},
	{"org.gnome.Nautilus.desktop", 30 * time.Second},
	{"org.gnome.TextEditor.desktop", 20 * time.Second},
	{"org.gnome.Calculator.desktop", 20 * time.Second},
	{"org.gnome.Loupe.desktop", 20 * time.Second},
	{"org.gnome.SystemMonitor.desktop", 20 * time.Second},
	{"org.gnome.DiskUtility.desktop", 20 * time.Second},
	{"firefox.desktop", 60 * time.Second},
}

// panels are Settings' pages a person reaches first; each is opened by name and screenshotted.
var panels = []string{"wifi", "network", "bluetooth", "display", "sound", "power", "keyboard", "users", "system"}

func test(args []string) error {
	fl := flag.NewFlagSet("test", flag.ExitOnError)
	only := fl.String("only", "", "comma-separated scenario names (app ids or panel:<name>); all by default")
	mem := fl.String("m", "6G", "memory")
	_ = fl.Parse(args)

	dir := filepath.Join(out, "test")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, pub, err := testKey()
	if err != nil {
		return err
	}
	v, err := start(*mem, pub)
	if err != nil {
		return err
	}
	defer v.stop()

	r := &report{dir: dir}
	g := &guest{cid: v.cid}
	if err := g.wait(4 * time.Minute); err != nil {
		return fmt.Errorf("guest never answered over vsock: %w", err)
	}
	r.add("boot", true, "sshd over vsock answered")
	if _, err := g.waitShell(3 * time.Minute); err != nil {
		r.add("shell", false, err.Error())
		_ = v.screenshot(filepath.Join(dir, "shell.ppm"))
		r.write(g)
		return errors.New("GNOME Shell never answered Eval")
	}
	r.add("shell", true, "org.gnome.Shell.Eval answers in unsafe mode")
	r.shot(v, "desktop")

	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n != "" {
			want[n] = true
		}
	}
	for _, a := range apps {
		if len(want) > 0 && !want[a.id] {
			continue
		}
		ok, detail := g.runApp(a)
		r.add(a.id, ok, detail)
		r.shot(v, strings.TrimSuffix(a.id, ".desktop"))
		_, _ = g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q)?.request_quit(); true`, a.id))
		time.Sleep(2 * time.Second)
	}
	for _, p := range panels {
		if len(want) > 0 && !want["panel:"+p] {
			continue
		}
		ok, detail := g.runPanel(p)
		r.add("panel:"+p, ok, detail)
		r.shot(v, "panel-"+p)
	}
	_, _ = g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop")?.request_quit(); true`)
	return r.write(g)
}

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
		block, merr := ssh.MarshalPrivateKey(priv, "spin-desktop test")
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
const evalScript = `js=$(cat); exec sudo -u spin env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus ` +
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
		val = outs[i+1 : j]
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

func (g *guest) runApp(a app) (bool, string) {
	v, err := g.eval(fmt.Sprintf(`const a = Shell.AppSystem.get_default().lookup_app(%q); a ? (a.activate(), "launched") : "missing"`, a.id))
	if err != nil {
		return false, err.Error()
	}
	if v != `"launched"` {
		return false, "not installed (no desktop file)"
	}
	deadline := time.Now().Add(a.timeout)
	for time.Now().Before(deadline) {
		n, err := g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q).get_windows().length`, a.id))
		if err == nil && n != "0" {
			title, _ := g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q).get_windows()[0].get_title()`, a.id))
			return true, "window " + title
		}
		time.Sleep(time.Second)
	}
	return false, fmt.Sprintf("no window within %v", a.timeout)
}

func (g *guest) runPanel(p string) (bool, string) {
	if _, err := g.eval(fmt.Sprintf(`GLib.spawn_command_line_async("gnome-control-center %s")`, p)); err != nil {
		return false, err.Error()
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n, err := g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").get_windows().length`)
		if err == nil && n != "0" {
			time.Sleep(3 * time.Second) // the panel fills in after the window maps
			return true, "opened"
		}
		time.Sleep(time.Second)
	}
	return false, "Settings opened no window"
}

type result struct {
	name, detail string
	ok           bool
}

type report struct {
	dir     string
	results []result
}

func (r *report) add(name string, ok bool, detail string) {
	r.results = append(r.results, result{name, detail, ok})
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	fmt.Printf("%s %-36s %s\n", mark, name, detail)
}

func (r *report) shot(v *vm, name string) {
	ppm := filepath.Join(r.dir, name+".ppm")
	if err := v.screenshot(ppm); err != nil {
		return
	}
	time.Sleep(500 * time.Millisecond)
	if err := ppmToPNG(ppm, strings.TrimSuffix(ppm, ".ppm")+".png"); err == nil {
		_ = os.Remove(ppm)
	}
}

// write adds what the system says about itself - failed units, core dumps, the journal's errors
// - and writes the report; it fails when any scenario or system check did.
func (r *report) write(g *guest) error {
	failed, _ := g.run(`systemctl --failed --no-legend --plain | awk '{print $1}'`, "")
	userFailed, _ := g.run(`systemctl --user -M spin@ --failed --no-legend --plain 2>/dev/null | awk '{print $1}'`, "")
	dumps, _ := g.run(`coredumpctl --no-pager --no-legend list 2>/dev/null | awk '{print $NF}' | sort | uniq -c`, "")
	errs, _ := g.run(`journalctl -b -p err --no-pager -o short-monotonic`, "")
	r.add("system units", strings.TrimSpace(failed) == "", "failed: "+oneLine(failed))
	r.add("user units", strings.TrimSpace(userFailed) == "", "failed: "+oneLine(userFailed))
	r.add("core dumps", strings.TrimSpace(dumps) == "", oneLine(dumps))
	_ = os.WriteFile(filepath.Join(r.dir, "journal-errors.txt"), []byte(errs), 0o644)

	var b strings.Builder
	bad := 0
	for _, x := range r.results {
		mark := "PASS"
		if !x.ok {
			mark = "FAIL"
			bad++
		}
		fmt.Fprintf(&b, "%s %s: %s\n", mark, x.name, x.detail)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "report.txt"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d/%d passed; screenshots and journal-errors.txt in %s\n", len(r.results)-bad, len(r.results), r.dir)
	if bad > 0 {
		return fmt.Errorf("%d checks failed", bad)
	}
	return nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ppmToPNG converts QEMU's screendump (binary P6, 8-bit) to PNG.
func ppmToPNG(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var fields []int
	var magic string
	for len(fields) < 3 {
		tok, err := ppmToken(br)
		if err != nil {
			return err
		}
		if magic == "" {
			magic = tok
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return err
		}
		fields = append(fields, n)
	}
	if magic != "P6" || fields[2] != 255 {
		return fmt.Errorf("%s: not an 8-bit P6", src)
	}
	w, h := fields[0], fields[1]
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(br, pix); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Set(i%w, i/w, color.RGBA{pix[3*i], pix[3*i+1], pix[3*i+2], 255})
	}
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := png.Encode(o, img); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// ppmToken reads one whitespace-separated header token, and the single whitespace after it.
func ppmToken(br *bufio.Reader) (string, error) {
	var tok []byte
	for {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '#' {
			_, _ = br.ReadString('\n')
			continue
		}
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			if len(tok) > 0 {
				return string(tok), nil
			}
			continue
		}
		tok = append(tok, c)
	}
}
