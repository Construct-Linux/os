package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	{"code.desktop", 40 * time.Second},
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
	m, err := testMachine(*mem, pub, *stick)
	if err != nil {
		return err
	}
	v, g, err := m.bootGuest(4 * time.Minute)
	if err != nil {
		return err
	}
	defer v.stop()

	r := &report{dir: dir, user: "live"}
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
	r.installer(v, g, *stick)
	// The live user's login keyring has no password (drift live): an app's first secret asks
	// for none.
	keyring, _ := g.run(`sudo -u live env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus `+
		`busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login org.freedesktop.Secret.Collection Locked 2>&1`, "")
	r.add("the login keyring is open", strings.TrimSpace(keyring) == "b false", oneLine(keyring))
	if *stick {
		r.secureBoot(g)
	}
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
	// Video on the GPU: the VM has no Intel GPU to decode with, but Iris Xe's driver must be where
	// libva looks, and GStreamer must load its va plugin.
	va, err := g.run(`test -f /usr/lib/dri/iHD_drv_video.so && gst-inspect-1.0 va 2>&1 | grep -m1 Filename`, "")
	r.add("vaapi", err == nil && strings.Contains(va, "libgstva.so"), oneLine(va)+errText(err))
	// Visual Studio Code: its command line answers (it needs no display).
	code, err := g.run(`sudo -u live code --version 2>&1 | head -1`, "")
	r.add("vscode", err == nil && strings.HasPrefix(strings.TrimSpace(code), "1."), oneLine(code)+errText(err))
	// Printing: cupsd starts when asked, through its socket.
	lp, err := g.run(`lpstat -r 2>&1`, "")
	r.add("printing", err == nil && strings.Contains(lp, "scheduler is running"), oneLine(lp)+errText(err))
	// Firmware and the system's own updates, which GNOME Software reaches over the bus.
	bus, err := g.run(`busctl --system list --activatable --no-legend | awk '{print $1}' | grep -xE 'org.freedesktop.fwupd|org.freedesktop.sysupdate1' | sort | tr '\n' ' '`, "")
	r.add("updates services", err == nil && strings.TrimSpace(bus) == "org.freedesktop.fwupd org.freedesktop.sysupdate1", oneLine(bus)+errText(err))
	// The Wolfi container Ptyxis's Wolfi profile enters: made on Docker, apk inside it.
	box, err := g.run(`cd /tmp && sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 timeout 600 distrobox create --yes >/dev/null 2>&1 && sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 timeout 600 distrobox enter wolfi -- apk --version 2>&1 | tail -1`, "")
	r.add("wolfi container", err == nil && strings.HasPrefix(strings.TrimSpace(box), "apk-tools"), oneLine(box)+errText(err))
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
	// A name -only gives that nothing answers to would run nothing, and pass.
	known := map[string]bool{"views": true}
	for _, a := range apps {
		known[a.id] = true
	}
	for _, p := range panels {
		known["panel:"+p] = true
	}
	for n := range want {
		if !known[n] {
			r.add("-only "+n, false, "no app, panel:<name> or views is called that")
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

// secureBoot checks the machine came up with Secure Boot on, in user mode - the keys systemd-boot
// enrolled from the ESP - and the kernel locked down for it.
func (r *report) secureBoot(g *guest) {
	out, _ := g.run(`bootctl status 2>/dev/null | grep -E 'Secure Boot:'; cat /sys/kernel/security/lockdown`, "")
	r.add("secure boot", strings.Contains(out, "Secure Boot: enabled (user)") && strings.Contains(out, "[integrity]"), oneLine(out))
}

// installerWindows counts the graphical installer's windows on the live session's screen.
const installerWindows = `global.get_window_actors().map(a => a.meta_window).filter(w => w.get_wm_class() === "org.spindrift.Installer")`

// installer checks the live session offers the graphical installer at login on a stick, and
// nowhere else; its window is shot and closed, so it covers nothing after.
func (r *report) installer(v *vm, g *guest, stick bool) {
	n := "0"
	deadline := time.Now().Add(time.Minute)
	for stick && time.Now().Before(deadline) {
		if n, _ = g.eval(`String(` + installerWindows + `.length)`); strings.Trim(n, `"`) == "1" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !stick {
		n, _ = g.eval(`String(` + installerWindows + `.length)`)
	}
	want := map[bool]string{true: "1", false: "0"}[stick]
	r.add("installer offered", strings.Trim(n, `"`) == want, fmt.Sprintf("%s installer windows on a stick=%v", n, stick))
	// Its pages, walked as a person does (image/test/rootfs/usr/lib/spindrift-test).
	walked, err := g.run(`sudo -u live env XDG_RUNTIME_DIR=/run/user/1000 WAYLAND_DISPLAY=wayland-0 `+
		`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus /usr/lib/spindrift-test/walk-installer.py 2>&1`, "")
	r.add("installer's pages", err == nil && strings.Contains(walked, "walked"), oneLine(walked)+errText(err))
	if stick && strings.Trim(n, `"`) == "1" {
		time.Sleep(2 * time.Second)
		r.shot(v, "installer")
		_, _ = g.eval(installerWindows + `.forEach(w => w.delete(global.get_current_time())); true`)
		time.Sleep(time.Second)
	}
}
