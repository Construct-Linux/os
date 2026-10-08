package main

import (
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
	focused := false
	for range 20 {
		time.Sleep(time.Second)
		n, _ := g.eval(`String(global.get_window_actors().map(a => a.meta_window).filter(w => w.get_title() === "` + keydProbeTitle + `").map(w => (Main.activateWindow(w), w.has_focus())).join())`)
		if strings.Trim(n, `"`) == "true" {
			focused = true
			break
		}
	}
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
	overview, _ := g.eval(`String(Main.overview.visible)`)
	var got string
	for range 60 {
		if got, err = g.run(`cat `+keydProbeOut, ""); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	r.add("keyd: Super+V, A, C, X", got == "mkjkj" && strings.Trim(overview, `"`) == "false",
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
	for range 30 {
		time.Sleep(time.Second)
		if mounted, _ = g.run(`awk '$2 ~ /USBTEST$/ {print $2}' /proc/mounts`, ""); strings.TrimSpace(mounted) != "" {
			break
		}
	}
	mounted = strings.TrimSpace(mounted)
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
// labelled label, as the accessibility bus has the session's labels - nothing else is open - and
// says whether it came to that.
func (r *report) desktopHasIcon(g *guest, label string, want bool) bool {
	for range 20 {
		time.Sleep(time.Second)
		out, _ := g.run(asLive+`python3 -c '
import gi
gi.require_version("Atspi", "2.0")
from gi.repository import Atspi
def walk(a, d=0):
    if a is None or d > 30: return False
    try:
        if a.get_name() == "`+label+`": return True
        return any(walk(a.get_child_at_index(i), d + 1) for i in range(a.get_child_count()))
    except Exception: return False
desk = Atspi.get_desktop(0)
print(any(walk(desk.get_child_at_index(i)) for i in range(desk.get_child_count())))
'`, "")
		if (strings.TrimSpace(out) == "True") == want {
			return true
		}
	}
	return false
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
