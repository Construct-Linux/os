package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	{"org.gnome.Showtime.desktop", 30 * time.Second},
	{"org.gnome.Decibels.desktop", 30 * time.Second},
	{"org.gnome.Music.desktop", 30 * time.Second},
	{"org.gnome.clocks.desktop", 20 * time.Second},
	{"org.gnome.Weather.desktop", 30 * time.Second},
	{"org.gnome.Characters.desktop", 20 * time.Second},
	{"org.gnome.font-viewer.desktop", 20 * time.Second},
	{"org.gnome.baobab.desktop", 20 * time.Second},
	{"org.gnome.Logs.desktop", 20 * time.Second},
	{"org.gnome.Contacts.desktop", 30 * time.Second},
	{"org.gnome.Maps.desktop", 30 * time.Second},
	{"org.gnome.Connections.desktop", 20 * time.Second},
	{"org.gnome.Software.desktop", 60 * time.Second},
	{"org.gnome.Papers.desktop", 20 * time.Second},
	{"org.gnome.Calendar.desktop", 30 * time.Second},
	{"firefox.desktop", 60 * time.Second},
	{"com.google.Chrome.desktop", 60 * time.Second},
	// Last: it asks for the camera, a shell dialog over whatever opens after it.
	{"org.gnome.Snapshot.desktop", 30 * time.Second},
}

// panels are Settings' pages a person reaches first; each is opened by name and screenshotted.
var panels = []string{"wifi", "network", "bluetooth", "display", "sound", "power", "keyboard", "users", "system"}

func test(args []string) error {
	fl := flag.NewFlagSet("test", flag.ExitOnError)
	only := fl.String("only", "", "comma-separated scenario names (app ids, panel:<name>, views); all by default")
	mem := fl.String("m", "6G", "memory")
	stick := fl.Bool("usb", false, "boot the USB disk (task usb -- -test) as a machine does, instead of the ext4 test disk")
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
	v, err := startDisk(*mem, pub, *stick)
	if err != nil {
		return err
	}
	defer v.stop()

	r := &report{dir: dir, user: "live"}
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
	// The run takes longer than GNOME's idle delay: with the display blanked, every screenshot
	// after it is a black "Display output is not active".
	_, _ = g.run(`sudo -u live DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gsettings set org.gnome.desktop.session idle-delay 0`, "")
	_, _ = g.eval(`Main.overview.hide(); true`)
	time.Sleep(time.Second)
	r.shot(v, "desktop")
	sinks, err := g.run(`sudo -u live XDG_RUNTIME_DIR=/run/user/1000 wpctl status 2>&1 | sed -n '/Sinks:/,/Sources:/p'`, "")
	r.add("audio", err == nil && strings.Contains(sinks, "Built-in Audio"), oneLine(sinks)+errText(err))
	sched, err := g.run("cat /sys/kernel/sched_ext/state /sys/kernel/sched_ext/root/ops 2>&1", "")
	r.add("sched_ext", err == nil && strings.HasPrefix(sched, "enabled"), oneLine(sched)+errText(err))
	for _, e := range g.extensions() {
		r.add("extension "+e.uuid, e.ok(), e.detail())
	}
	ok, detail := g.notifications()
	r.add("notifications", ok, detail)
	if res, err := g.run(`getent hosts dl.flathub.org`, ""); err == nil && strings.TrimSpace(res) != "" {
		r.add("dns", true, oneLine(res))
	} else {
		diag, _ := g.run(`ls -l /etc/resolv.conf; echo ---; cat /etc/resolv.conf; echo ---; ls -l /run/NetworkManager/ 2>&1; cat /run/NetworkManager/resolv.conf 2>&1; echo ---; systemctl is-active systemd-resolved; resolvectl status 2>&1 | head -20; echo ---; NetworkManager --print-config 2>/dev/null | grep -A3 -i '^\[main\]'; nmcli dev show 2>&1 | grep -i dns`, "")
		_ = os.WriteFile(filepath.Join(dir, "dns.txt"), []byte(diag), 0o644)
		r.add("dns", false, "no answer for dl.flathub.org; see dns.txt")
	}
	dock, err := g.run(`sudo -u live docker run --rm cgr.dev/chainguard/wolfi-base echo container-ran 2>&1`, "")
	_ = os.WriteFile(filepath.Join(dir, "docker.txt"), []byte(dock), 0o644)
	r.add("docker", err == nil && strings.Contains(dock, "container-ran"), lastLine(dock))
	// NetworkManager's Wi-Fi is wpa_supplicant, started by the bus when it is asked for: a VM has
	// no Wi-Fi card to join a network with, but the supplicant must be there to be started.
	wifi, err := g.run(`busctl call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus StartServiceByName su fi.w1.wpa_supplicant1 0 2>&1`, "")
	r.add("wifi supplicant", err == nil && strings.HasPrefix(strings.TrimSpace(wifi), "u "), oneLine(wifi)+errText(err))
	// Bluetooth, the same way: bluetoothd starts only with an adapter, which a VM has not, but the
	// bus must know how to start it (Wolfi's bluez came without its unit and activation file).
	bt, err := g.run(`busctl --system list --activatable --no-legend | awk '{print $1}' | grep -x org.bluez`, "")
	r.add("bluetooth service", err == nil && strings.TrimSpace(bt) == "org.bluez", oneLine(bt)+errText(err))
	ok, detail = g.waitUnit("spindrift-flathub.service", 15*time.Minute)
	r.add("flathub", ok, detail)
	// libadwaita apps, the image's and Flathub's, take Orchis from ~/.config/gtk-4.0: real files
	// (drift gtk4), which a Flatpak reads through the image's read-only override. The marker is
	// the dark styles' media query, which only the Orchis fork's light gtk.css carries.
	gtk4, err := g.run(`f=/var/home/live/.config/gtk-4.0/gtk.css; test -f "$f" && ! test -L "$f" && sudo -u live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus flatpak run --command=sh com.google.Chrome -c 'grep -c "prefers-color-scheme" "$HOME/.config/gtk-4.0/gtk.css"' 2>&1`, "")
	r.add("gtk4 theme in flatpak", err == nil && strings.TrimSpace(lastLine(gtk4)) != "0", "Chrome's sandbox reads the copied gtk.css: "+lastLine(gtk4))

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
		// A window exists before it is drawn: Software loads its catalogue, Calendar its sources,
		// Maps its tiles. The screenshot is what a person sees a few seconds in.
		time.Sleep(5 * time.Second)
		r.shot(v, strings.TrimSuffix(a.id, ".desktop"))
		_, _ = g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q)?.request_quit(); true`, a.id))
		// A shell dialog an app left open - the camera permission Snapshot asks for - outlives the
		// app and would cover every screenshot after it.
		_, _ = g.eval(`Main.layoutManager.modalDialogGroup.get_children().forEach(d => d.close?.()); true`)
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
	if len(want) == 0 || want["views"] {
		r.shellViews(v, g)
	}
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
	dir string
	// user is whose session the user-unit check reads: live on the test disk; none on the
	// lifecycle's installed machine, where GDM waits for the person's password.
	user    string
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
	if g != nil {
		r.system(g)
	}
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

// system adds what the machine says about itself: failed units, core dumps, the journal's errors.
//
// Each check passes only on an answer: a guest that no longer answers over SSH says nothing, which
// is not that nothing failed.
func (r *report) system(g *guest) {
	failed, failedErr := g.run(`systemctl --failed --no-legend --plain | awk '{print $1}'`, "")
	// EXE is the next-to-last column; the last is the core's size.
	dumps, dumpsErr := g.run(`coredumpctl --no-pager --no-legend list 2>/dev/null | awk '{print $(NF-1)}' | sort | uniq -c`, "")
	if strings.TrimSpace(dumps) != "" {
		info, _ := g.run(`coredumpctl --no-pager info 2>&1`, "")
		_ = os.WriteFile(filepath.Join(r.dir, "coredumps.txt"), []byte(info), 0o644)
	}
	errs, _ := g.run(`journalctl -b -p err --no-pager -o short-monotonic`, "")
	// What GNOME Shell logs below error priority but is a bug all the same: an extension's
	// exception, an actor used after it was destroyed (both typical of a lock/unlock cycle, which
	// disables and enables every extension), a cursor the theme lacks.
	shell, shellErr := g.run(`journalctl -b --no-pager -o cat _COMM=gnome-shell | grep -E 'JS ERROR|already disposed|Could not load cursor' || true`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "shell-log.txt"), []byte(shell), 0o644)
	lines := strings.Fields(strings.ReplaceAll(strings.TrimSpace(shell), " ", "_"))
	r.add("shell log", shellErr == nil && len(lines) == 0, fmt.Sprintf("%d lines in shell-log.txt: %s", len(lines), oneLine(firstLine(shell)))+errText(shellErr))
	r.add("system units", failedErr == nil && strings.TrimSpace(failed) == "", "failed: "+oneLine(failed)+errText(failedErr))
	// Each failed unit's own log, while the machine that has it is still up.
	units := filepath.Join(r.dir, "units")
	for _, u := range strings.Fields(failed) {
		log, _ := g.run("journalctl -b --no-pager -n 60 -u "+u, "")
		_ = os.MkdirAll(units, 0o755)
		_ = os.WriteFile(filepath.Join(units, u+".log"), []byte(log), 0o644)
	}
	if r.user != "" {
		userFailed, err := g.run(`set -o pipefail; systemctl --user -M `+r.user+`@ --failed --no-legend --plain | awk '{print $1}'`, "")
		r.add("user units", err == nil && strings.TrimSpace(userFailed) == "", "failed: "+oneLine(userFailed)+errText(err))
	}
	r.add("core dumps", dumpsErr == nil && strings.TrimSpace(dumps) == "", oneLine(dumps)+errText(dumpsErr))
	// A PAM file that includes one the image does not have fails every authentication through it,
	// and says so only in the journal.
	pam, pamErr := g.run(`journalctl -b --no-pager -o cat | grep -E 'unable to open config for' | sort -u || true`, "")
	r.add("pam", pamErr == nil && strings.TrimSpace(pam) == "", oneLine(pam)+errText(pamErr))
	_ = os.WriteFile(filepath.Join(r.dir, "journal-errors.txt"), []byte(errs), 0o644)
	// Plymouth's own log, when the disk was built to boot with plymouth.debug.
	if ply, err := g.run("cat /var/log/plymouth-debug.log", ""); err == nil && ply != "" {
		_ = os.WriteFile(filepath.Join(r.dir, "plymouth-debug.log"), []byte(ply), 0o644)
	}

}

func firstLine(s string) string {
	return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
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
	var v *vm
	if *vars != "" {
		m := machine{mem: "6G", pubkey: pub, vars: *vars, nvme: *nvme}
		if *sticks != "" {
			m.sticks = strings.Split(*sticks, ",")
		}
		v, err = m.start()
	} else {
		v, err = startDisk("6G", pub, *stick)
	}
	if err != nil {
		return err
	}
	defer v.stop()
	g := &guest{cid: v.cid}
	if err := g.wait(4 * time.Minute); err != nil {
		return err
	}
	time.Sleep(*wait)
	outs, err := g.run(strings.Join(fl.Args(), " "), "")
	fmt.Print(outs)
	return err
}

// extension is one GNOME Shell extension the session enables, as the shell's extension manager
// sees it.
type extension struct {
	uuid, err string
	state     int // ExtensionState: 1 active, 3 error, 4 out of date (misc/extensionUtils.js)
	found     bool
}

func (e extension) ok() bool { return e.found && e.state == 1 }

func (e extension) detail() string {
	switch {
	case !e.found:
		return "enabled, but the shell does not know it: not installed"
	case e.state == 1:
		return "active"
	case e.state == 4:
		return "out of date: its metadata.json does not list this shell's version"
	default:
		return fmt.Sprintf("state %d: %s", e.state, e.err)
	}
}

// extensions are the extensions the test user's session enables (org.gnome.shell
// enabled-extensions, which the image's dconf defaults set), each with the state the running shell
// gives it: an extension that broke with this shell, or is not there, fails the test.
func (g *guest) extensions() []extension {
	list, err := g.run(`sudo -u live DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gsettings get org.gnome.shell enabled-extensions`, "")
	if err != nil {
		return []extension{{uuid: "(enabled-extensions)", err: err.Error()}}
	}
	var enabled []string
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == '[' || r == ']' || r == ',' || r == ' ' || r == '\n' }) {
		if f = strings.Trim(f, `'"`); f != "" && f != "@as" {
			enabled = append(enabled, f)
		}
	}
	raw, err := g.eval(`JSON.stringify(Main.extensionManager.getUuids().map(u => { const e = Main.extensionManager.lookup(u); return [u, e.state, e.error ? String(e.error) : ""]; }))`)
	if err != nil {
		return []extension{{uuid: "(extension manager)", err: err.Error()}}
	}
	// Eval answers the JSON of its result: here a string, which holds the JSON of the list.
	var inner string
	var rows [][]any
	if json.Unmarshal([]byte(raw), &inner) != nil || json.Unmarshal([]byte(inner), &rows) != nil {
		return []extension{{uuid: "(extension manager)", err: "unreadable: " + raw}}
	}
	known := map[string]extension{}
	for _, row := range rows {
		if len(row) != 3 {
			continue
		}
		uuid, _ := row[0].(string)
		state, _ := row[1].(float64)
		msg, _ := row[2].(string)
		known[uuid] = extension{uuid: uuid, state: int(state), err: msg, found: true}
	}
	out := make([]extension, 0, len(enabled))
	for _, u := range enabled {
		if e, ok := known[u]; ok {
			out = append(out, e)
		} else {
			out = append(out, extension{uuid: u})
		}
	}
	return out
}

// notifications fails a login that greets the user with a notification: a fresh account on a
// fresh image has nothing to be told, so one there is a component announcing itself (Dash to
// Panel's "has been updated" did, on every first login).
func (g *guest) notifications() (bool, string) {
	raw, err := g.eval(`JSON.stringify(Main.messageTray.getSources().flatMap(s => s.notifications.map(n => s.title + ": " + n.title)))`)
	if err != nil {
		return false, err.Error()
	}
	var inner string
	var titles []string
	if json.Unmarshal([]byte(raw), &inner) != nil || json.Unmarshal([]byte(inner), &titles) != nil {
		return false, "unreadable: " + raw
	}
	if len(titles) > 0 {
		return false, strings.Join(titles, "; ")
	}
	return true, "none at login"
}

// shellView is a part of the shell drawn only by the shell theme: a screenshot of each is how a
// theme written for an older shell shows what it does not style. wake is input sent before the
// screenshot: a locked session turns its display off at once, and only input brings it back.
type shellView struct{ name, open, close, wake string }

// shellViews ends with the lock screen: its prompt is the unlock dialog the shell's AuthPrompt
// draws, which GNOME 51 restyled, and nothing after it in the run must find the session locked.
var shellViews = []shellView{
	{"overview", `Main.overview.show()`, `Main.overview.hide()`, ""},
	{"overview-search", `Main.overview.show(); Main.overview.searchEntry.set_text("se")`, `Main.overview.searchEntry.set_text(""); Main.overview.hide()`, ""},
	{"app-grid", `Main.overview.showApps()`, `Main.overview.hide()`, ""},
	{"quick-settings", `Main.panel.statusArea.quickSettings.menu.open()`, `Main.panel.statusArea.quickSettings.menu.close()`, ""},
	{"calendar", `Main.panel.statusArea.dateMenu.menu.open()`, `Main.panel.statusArea.dateMenu.menu.close()`, ""},
	// libadwaita apps must follow the light/dark switch: Settings, opened in the light scheme.
	{"settings-light",
		`new imports.gi.Gio.Settings({schema_id: "org.gnome.desktop.interface"}).set_string("color-scheme", "default"); Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").activate()`,
		`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").request_quit(); new imports.gi.Gio.Settings({schema_id: "org.gnome.desktop.interface"}).set_string("color-scheme", "prefer-dark")`, ""},
	{"lock-screen", `Main.screenShield.lock(false)`, ``, "pointer"},
	{"lock-prompt", `Main.screenShield.showDialog()`, `Main.screenShield.deactivate(false)`, "key"},
}

func (r *report) shellViews(v *vm, g *guest) {
	for _, s := range shellViews {
		_, err := g.eval(s.open + "; true")
		r.add("view:"+s.name, err == nil, "screenshot shell-"+s.name+".png")
		// Menus open in under a second; a window (Settings) needs a few to draw.
		time.Sleep(4 * time.Second)
		switch s.wake {
		case "pointer":
			_ = v.pointTo(16000, 16000)
			_ = v.pointTo(16400, 16400)
			time.Sleep(2 * time.Second)
		case "key":
			// The first key wakes the display; the second lifts the curtain to the prompt, and a
			// modifier alone does not.
			_ = v.key("shift")
			time.Sleep(2 * time.Second)
			_ = v.key("spc")
			time.Sleep(2 * time.Second)
		}
		r.shot(v, "shell-"+s.name)
		if s.name == "lock-screen" {
			r.locked(g)
		}
		if s.close != "" {
			_, _ = g.eval(s.close + "; true")
			time.Sleep(time.Second)
		}
	}
}

// locked checks what a lock leaves behind: the shell disables every extension while the screen is
// locked, and a disabled extension must not keep its machinery running (Tiling Shell kept one
// tiling manager per monitor, with their display handlers and actors, upstream #559).
func (r *report) locked(g *guest) {
	n, err := g.eval(`String(Main.extensionManager.lookup("tilingshell@ferrarodomenico.com")?.stateObj?._tilingManagers?.length ?? 0)`)
	r.add("locked: tilingshell", err == nil && strings.Trim(n, `"`) == "0", "tiling managers alive while disabled: "+n)
}
