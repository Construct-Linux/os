package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/qemu"
)

// keydProbe is the window image/test's keyd-probe.py opens, and where it writes what it held.
const (
	keydProbeTitle = "construct-keyd-probe"
	keydProbeOut   = "/run/user/1000/construct-test/keyd.txt"
)

// keyd checks Super with a letter does what etc/keyd/default.conf says, through everything a key
// goes through: QEMU's keyboard, the kernel, keyd's virtual keyboard, Mutter and GTK. A field
// starts empty with "k" on the clipboard: Super+V pastes it, j is typed, Super+A and Super+C copy
// "kj", End and Super+V make "kjkj", Super+A and Super+X cut it all, m is typed and Super+V pastes
// the cut. Only all four working leave "mkjkj": a cut that did not cut pastes "kj". And Super,
// pressed with a letter each time, never opened the overview.
func (r *report) keyd(v *qemu.VM, g *guest) {
	active, _ := g.run(`systemctl is-active keyd.service`, "")
	if strings.TrimSpace(active) != "active" {
		r.add("keyd", false, "keyd.service "+oneLine(active))
		return
	}
	_, _ = g.run(`rm -f `+keydProbeOut, "")
	_, err := g.eval(`const St = imports.gi.St; St.Clipboard.get_default().set_text(St.ClipboardType.CLIPBOARD, "k"); ` +
		`GLib.spawn_command_line_async("/usr/lib/construct-test/keyd-probe.py"); true`)
	if err != nil {
		r.add("keyd", false, "the probe: "+err.Error())
		return
	}
	focused := until(20*time.Second, time.Second, func() bool {
		var has string
		_ = g.evalJSON(`return global.get_window_actors().map(a => a.meta_window).filter(w => w.get_title() === "`+keydProbeTitle+`").map(w => (Main.activateWindow(w), w.has_focus())).join();`, &has)
		return has == "true"
	})
	if !focused {
		r.add("keyd", false, "the probe's window never had the focus")
		return
	}
	step := func(keys ...string) {
		_ = v.Key(keys...)
		time.Sleep(400 * time.Millisecond)
	}
	step("meta_l", "v")
	step("j")
	step("meta_l", "a")
	step("meta_l", "c")
	step("end")
	step("meta_l", "v")
	step("meta_l", "a")
	step("meta_l", "x")
	step("m")
	step("meta_l", "v")
	var overview string
	_ = g.evalJSON(`return String(Main.overview.visible);`, &overview)
	var got string
	until(60*time.Second, time.Second, func() bool { got, err = g.run(`cat `+keydProbeOut, ""); return err == nil })
	r.add("keyd: Super+V, A, C, X", got == "mkjkj" && overview == "false",
		fmt.Sprintf("the field held %q, want \"mkjkj\"; overview open %s", got, overview)+errText(err))
}

// usbStick plugs a stick into the running session as a person does: udisks sees it, GNOME mounts
// it under /run/media/live, and the desktop shows it (gtk4-ding: mounted volumes only). It is
// ejected and pulled out after, as a person does - pulled out mounted, the kernel logs the FAT it
// could not mark clean - and its icon goes, before any view is shot.
func (r *report) usbStick(v *qemu.VM, g *guest) {
	img := filepath.Join(r.dir, "usb-stick.img")
	_ = os.Remove(img)
	defer os.Remove(img)
	if out, err := exec.Command("mkfs.vfat", "-C", "-n", "USBTEST", img, "32768").CombinedOutput(); err != nil {
		r.add("usb stick", false, "mkfs.vfat: "+oneLine(string(out))+errText(err))
		return
	}
	abs, _ := filepath.Abs(img)
	if err := v.PlugUSB("usbtest", abs); err != nil {
		r.add("usb stick", false, err.Error())
		return
	}
	var mounted string
	until(30*time.Second, time.Second, func() bool {
		out, _ := g.run(`awk '$2 ~ /USBTEST$/ {print $2}' /proc/mounts`, "")
		mounted = strings.TrimSpace(out)
		return mounted != ""
	})
	r.add("usb stick mounts by itself", mounted == "/run/media/live/USBTEST", "mounted at "+fmt.Sprintf("%q", mounted))
	// gtk4-ding's window draws the desktop's icons, each a GTK widget labelled with the volume's
	// name: the accessibility tree is how another process reads what a GTK 4 window shows.
	icon := r.desktopHasIcon(g, "USBTEST", true)
	r.add("usb stick on the desktop", icon, "an icon labelled USBTEST on the desktop")
	ejected, err := g.run(asLive+`gio mount -e /run/media/live/USBTEST 2>&1`, "")
	r.add("usb stick ejects", err == nil, oneLine(ejected)+errText(err))
	gone := r.desktopHasIcon(g, "USBTEST", false)
	_ = v.UnplugUSB("usbtest")
	r.add("usb stick gone from the desktop", gone, "its icon gone once it is pulled out")
}

// desktopHasIcon waits up to 20 s for the desktop to show (want) or stop showing (!want) an icon
// labelled label - nothing else is open to show one (image/test's a11y-label.py) - and says
// whether it came to that.
func (r *report) desktopHasIcon(g *guest, label string, want bool) bool {
	answer := map[bool]string{true: "yes", false: "no"}[want]
	return until(20*time.Second, time.Second, func() bool {
		out, _ := g.run(asLive+`/usr/lib/construct-test/a11y-label.py `+label, "")
		return strings.TrimSpace(out) == answer
	})
}

// console checks Ctrl+Alt+F3 reaches a text login, as logind starts one there (getty@ answering
// as autovt@), and comes back to the session, whose VT it was on.
func (r *report) console(g *guest) {
	out, err := g.run(`back=$(cat /sys/class/tty/tty0/active); chvt 3; `+
		`for i in $(seq 20); do systemctl is-active -q autovt@tty3.service && break; sleep 0.5; done; `+
		`systemctl is-active autovt@tty3.service; chvt "${back#tty}"; echo back=$back`, "")
	r.add("text console on tty3", err == nil && strings.HasPrefix(strings.TrimSpace(out), "active"), oneLine(out)+errText(err))
}

// diagnose runs construct diagnose as a person does with sudo, without measuring the draw: it
// writes its archive, and the summary's sections are all there.
func (r *report) diagnose(g *guest) {
	out, err := g.run(`cd /tmp && construct diagnose -o /tmp --power-seconds 0 && tar tzf /tmp/construct-diagnose-*.tar.gz | sort; rm -f /tmp/construct-diagnose-*.tar.gz`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "diagnose.txt"), []byte(out), 0o644)
	ok := err == nil
	for _, want := range []string{"PCI devices with no driver:", "Errors this boot, by unit:", "Batteries:", "/summary.txt", "/hardware/lspci.txt", "/boot/plot.svg"} {
		ok = ok && strings.Contains(out, want)
	}
	r.add("construct diagnose", ok, "diagnose.txt"+errText(err))
}

// appFolder opens a folder of the app grid, as a click does, and checks every app it holds is
// drawn whole inside its dialog: GNOME Shell 51 sizes the dialog from the stylesheet, and a theme
// that set no size cut the apps off under the folder's name.
func (r *report) appFolder(g *guest) {
	type rect struct{ X, Y, W, H int }
	var got struct {
		Err   string
		Box   *rect
		Tiles []rect
	}
	err := g.evalJSON(`
		const wait = ms => new Promise(r => GLib.timeout_add(GLib.PRIORITY_DEFAULT, ms, () => { r(); return GLib.SOURCE_REMOVE; }));
		Main.overview.showApps();
		await wait(1500);
		const f = lfAll(Main.overview._overview, lfClass('app-folder')).find(a => a.is_mapped() && a.reactive);
		if (!f) { Main.overview.hide(); return {err: 'no folder in the app grid'}; }
		f.emit('clicked', 1);
		await wait(2000);
		const d = lfAll(global.stage, lfClass('app-folder-dialog')).find(a => a.is_mapped());
		const res = d ? {box: lfBox(d), tiles: lfAll(d, lfClass('overview-tile')).filter(a => a.is_mapped()).map(lfBox)} : {err: 'no dialog opened'};
		Main.overview.hide();
		await wait(1000);
		return res;`, &got)
	if err == nil && got.Err != "" {
		err = errors.New(got.Err)
	}
	inside := err == nil && got.Box != nil && len(got.Tiles) > 0
	var cut []rect
	for _, t := range got.Tiles {
		if b := got.Box; b != nil && (t.Y < b.Y || t.Y+t.H > b.Y+b.H || t.X < b.X || t.X+t.W > b.X+b.W) {
			cut = append(cut, t)
		}
	}
	r.add("an app folder shows its apps", inside && len(cut) == 0,
		fmt.Sprintf("dialog %+v, %d apps, cut off: %+v", got.Box, len(got.Tiles), cut)+errText(err))
}

// screencast records two seconds of the screen through the shell's own screencast proxy, as the
// screenshot UI's record button does, and checks a file came of it: the service registers only
// when GStreamer has the fallback pipeline's encoder (vp8enc), and without it the shell waited out
// its activation and offered no recording at all.
func (r *report) screencast(g *guest) {
	var got struct {
		Supported bool
		OK        bool
		File      string
		Err       string
	}
	err := g.evalJSON(`
		const wait = ms => new Promise(r => GLib.timeout_add(GLib.PRIORITY_DEFAULT, ms, () => { r(); return GLib.SOURCE_REMOVE; }));
		const ui = Main.screenshotUI;
		// The shell learns it can record once its proxy reaches the service, which a slow start
		// can leave for after the session is up.
		for (let i = 0; i < 40 && !ui._screencastSupported; i++)
			await wait(500);
		const res = {supported: ui._screencastSupported};
		try {
			const [ok, file] = await ui._screencastProxy.ScreencastAsync('/tmp/construct-recording', {});
			Object.assign(res, {ok, file});
			await wait(2000);
			await ui._screencastProxy.StopScreencastAsync();
			await wait(1000);
		} catch (e) {
			res.err = String(e);
		}
		return res;`, &got)
	if err == nil && got.Err != "" {
		err = errors.New(got.Err)
	}
	size := ""
	if got.File != "" {
		size, _ = g.run(`stat -c %s `+got.File+`; rm -f `+got.File, "")
		size = strings.TrimSpace(strings.Split(size, "\n")[0])
	}
	r.add("screen recording", err == nil && got.Supported && got.OK && size != "" && size != "0",
		fmt.Sprintf("supported %v, recorded %v to %s, %s bytes", got.Supported, got.OK, got.File, size)+errText(err))
}

// screenReader turns the screen reader on as Settings' Accessibility page does, and checks Orca
// comes up and speaks - speech-dispatcher, which Orca starts on its first word, running its
// espeak-ng module - then turns it off again: without Orca in the image, the switch did nothing.
func (r *report) screenReader(g *guest) {
	gs := asLive + `gsettings set org.gnome.desktop.a11y.applications screen-reader-enabled `
	out, err := g.run(gs+`true; for i in $(seq 40); do `+
		asLive+`systemctl --user -q is-active orca.service && pgrep -u live -f sd_espeak-ng >/dev/null && break; sleep 0.5; done; `+
		asLive+`systemctl --user is-active orca.service; pgrep -u live -l -f 'speech-dispatcher|sd_espeak-ng' | sed 's/^[0-9]* //' | sort -u | tr '\n' ' '`, "")
	_, offErr := g.run(gs+`false; for i in $(seq 20); do `+asLive+`systemctl --user -q is-active orca.service || break; sleep 0.5; done`, "")
	r.add("the screen reader speaks", err == nil && offErr == nil && strings.HasPrefix(strings.TrimSpace(out), "active") &&
		strings.Contains(out, "sd_espeak-ng"), oneLine(out)+errText(err)+errText(offErr))
}
