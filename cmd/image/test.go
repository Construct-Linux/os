package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	{"com.mitchellh.ghostty.desktop", 20 * time.Second},
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
	only := fl.String("only", "", "comma-separated scenario names (app ids, panel:<name>, views, bench, suspend); all by default")
	mem := fl.String("m", "6G", "memory")
	stick := fl.Bool("usb", false, "boot the USB disk (task usb -- -test) as a machine does, instead of the ext4 test disk")
	needles := fl.String("needles", "", "a checkout of github.com/Construct-Linux/needles: every screenshot is compared with its needle")
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
	var v *vm
	var g *guest
	// A stick boots as a machine does, firmware, systemd-boot and Plymouth: it is filmed from the
	// start until the shell has started, for what the person sees on the way (lf.go, filmReport).
	var reel *film
	if *stick {
		if v, err = m.start(); err != nil {
			return err
		}
		reel = startFilm(v, filepath.Join(dir, "film"))
		g = &guest{cid: v.cid}
		if err := g.wait(4 * time.Minute); err != nil {
			reel.stop()
			v.stop()
			return fmt.Errorf("the guest never answered over vsock (its boot, filmed: %s): %w", filepath.Join(dir, "film"), err)
		}
	} else if v, g, err = m.bootGuest(4 * time.Minute); err != nil {
		return err
	}
	defer v.stop()

	r := &report{dir: dir, user: "live"}
	r.add("boot", true, "sshd over vsock answered")
	_, shellErr := g.waitShell(3 * time.Minute)
	var bootMark *[2]float64
	if reel != nil {
		bootMark = r.filmReport(reel.stop(), reel.failed)
	} else {
		r.skip("boot film", "the ext4 test disk boots its kernel without Plymouth: task test:usb films the boot")
	}
	if shellErr != nil {
		r.add("shell", false, shellErr.Error())
		_ = v.screenshot(filepath.Join(dir, "shell.ppm"))
		r.write(g)
		return errors.New("GNOME Shell never answered Eval")
	}
	r.add("shell", true, "org.gnome.Shell.Eval answers in unsafe mode")
	l := newLF(r, v, g)
	l.bootMark = bootMark
	// The run takes longer than GNOME's idle delay: with the display blanked, every screenshot
	// after it is a black "Display output is not active".
	_, _ = g.run(`sudo -u live DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gsettings set org.gnome.desktop.session idle-delay 0`, "")
	_, _ = g.eval(`Main.overview.hide(); true`)
	time.Sleep(time.Second)
	r.installer(v, g, l, *stick)
	// The live user's login keyring has no password (construct live): a secret is kept and read back
	// with no prompt. A plaintext keyring reads as locked until something uses it, so the check
	// does what an app does; a prompt would hang it until the timeout.
	keyring, _ := g.run(keyringRoundTrip("live", 1000), "")
	r.add("the login keyring is open", strings.TrimSpace(keyring) == "kept", oneLine(keyring))
	if *stick {
		r.secureBoot(g)
	}
	r.shotLook(v, g, "desktop")
	l.view("desktop")
	sinks, err := g.run(`sudo -u live XDG_RUNTIME_DIR=/run/user/1000 wpctl status 2>&1 | sed -n '/Sinks:/,/Sources:/p'`, "")
	r.add("audio", err == nil && strings.Contains(sinks, "Built-in Audio"), oneLine(sinks)+errText(err))
	sched, err := g.run("cat /sys/kernel/sched_ext/state /sys/kernel/sched_ext/root/ops 2>&1", "")
	r.add("sched_ext", err == nil && strings.HasPrefix(sched, "enabled"), oneLine(sched)+errText(err))
	// scx_loader runs it (etc/scx_loader.toml), and answers which over its bus.
	loader, err := g.run(`scxctl get 2>&1`, "")
	r.add("scx_loader runs lavd", err == nil && strings.Contains(strings.ToLower(loader), "running lavd"), oneLine(loader)+errText(err))
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
	// What waits for a person does not start with the machine: Docker and containerd come up when
	// docker's socket is first used, and nothing has used it yet.
	idle, _ := g.run(`systemctl is-active docker.service containerd.service`, "")
	r.add("on demand only", len(strings.Fields(idle)) == 2 && !slices.Contains(strings.Fields(idle), "active"), oneLine(idle))
	// The default terminal, as GLib and the session's "open a terminal" find it.
	term, _ := g.run(`sudo -u live XDG_RUNTIME_DIR=/run/user/1000 xdg-terminal-exec --print-id 2>&1`, "")
	r.add("default terminal", strings.Contains(term, "com.mitchellh.ghostty.desktop"), oneLine(term))
	// A machine nobody named is os-release's DEFAULT_HOSTNAME.
	name, err := g.run(`hostnamectl hostname`, "")
	r.add("the computer's default name", err == nil && strings.TrimSpace(name) == "construct", "hostname: "+oneLine(name)+errText(err))
	// .local names go through avahi, the one mDNS stack (nss-mdns): its own resolves, and nothing
	// else holds port 5353.
	local, err := g.run(`getent hosts "$(hostname).local"; ss -Hulnp 'sport = :5353' | grep -o 'users:(("[^"]*' | sort -u`, "")
	r.add("mdns", err == nil && strings.Contains(local, "construct.local") && strings.Contains(local, `"avahi-daemon`) && !strings.Contains(local, `"systemd-resolve`), oneLine(local)+errText(err))
	// The memory, writeback and network settings (usr/lib/sysctl.d/70-construct.conf, zram,
	// THP), as the running kernel has them.
	tune, _ := g.run(`swapon --show=NAME,TYPE --noheadings | tr -s ' ' | tr '\n' ';'; `+
		`sysctl -n vm.swappiness vm.page-cluster vm.vfs_cache_pressure vm.dirty_bytes net.ipv4.tcp_congestion_control kernel.nmi_watchdog | tr '\n' ' '; `+
		`cat /sys/kernel/mm/transparent_hugepage/defrag /sys/module/zswap/parameters/enabled | tr '\n' ' '`, "")
	r.add("memory and network settings", strings.Contains(tune, "/dev/zram0 partition;") &&
		strings.Contains(tune, "150 0 50 268435456 bbr 0") && strings.Contains(tune, "[defer+madvise]") && strings.HasSuffix(strings.TrimSpace(tune), "N"), oneLine(tune))
	// Tailscale's agent runs, waiting for someone to log in (no tailnet in the harness).
	ts, _ := g.run(`systemctl is-active tailscaled; tailscale status --json 2>/dev/null | grep -o '"BackendState": *"[A-Za-z]*"'`, "")
	r.add("tailscale", strings.HasPrefix(ts, "active") && strings.Contains(ts, "NeedsLogin"), oneLine(ts))
	// The firewall: a server listening on every address answers this machine, and not another on
	// the network - a network namespace behind a veth stands in for one, its packets arriving on an
	// interface that is neither loopback nor a container's bridge.
	fw, _ := g.run(`nft list chain inet construct input | grep -c 'policy drop'; `+
		`ip netns add fwtest && ip link add fw0 type veth peer name fw1 netns fwtest && `+
		`ip addr add 10.99.0.1/24 dev fw0 && ip link set fw0 up && `+
		`ip -n fwtest addr add 10.99.0.2/24 dev fw1 && ip -n fwtest link set fw1 up && `+
		// Python takes its time to listen: the local answer is waited for, up to ten seconds.
		`{ python3 -m http.server 8765 --bind 0.0.0.0 >/dev/null 2>&1 & p=$!; l=7; `+
		`for i in $(seq 20); do curl -s -o /dev/null -m 1 http://127.0.0.1:8765/ && { l=0; break; }; sleep 0.5; done; echo local=$l; `+
		`ip netns exec fwtest curl -s -o /dev/null -m 3 http://10.99.0.1:8765/; echo network=$?; kill $p; }; `+
		`ip netns del fwtest`, "")
	r.add("firewall", strings.HasPrefix(fw, "1\n") && strings.Contains(fw, "local=0") && strings.Contains(fw, "network=28"), oneLine(fw))
	dock, err := g.run(`sudo -u live docker run --rm cgr.dev/chainguard/wolfi-base echo container-ran 2>&1`, "")
	_ = os.WriteFile(filepath.Join(dir, "docker.txt"), []byte(dock), 0o644)
	r.add("docker", err == nil && strings.Contains(dock, "container-ran"), lastLine(dock))
	// A port Docker publishes without an address is this machine's (etc/docker/daemon.json).
	published, err := g.run(`c=$(sudo -u live docker run -d --rm -p 8766:8080 cgr.dev/chainguard/wolfi-base sleep 30) && sudo -u live docker port "$c"; sudo -u live docker rm -f "$c" >/dev/null 2>&1`, "")
	r.add("docker publishes on loopback", err == nil && strings.Contains(published, "127.0.0.1:8766") && !strings.Contains(published, "0.0.0.0"), oneLine(published))
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
	// A laptop's sensors and fingerprint reader, neither of which a VM has: fprintd starts when
	// the bus is asked for it, iio-sensor-proxy when udev finds a sensor.
	laptop, err := g.run(`busctl --system list --activatable --no-legend | awk '$1=="net.reactivated.Fprint"{print $1}'; `+
		`grep -l iio-sensor-proxy.service /usr/lib/udev/rules.d/*.rules`, "")
	r.add("laptop services", err == nil && strings.Contains(laptop, "net.reactivated.Fprint") && strings.Contains(laptop, "80-iio-sensor-proxy.rules"), oneLine(laptop)+errText(err))
	// The Wolfi container Ptyxis's Wolfi profile enters: made on Docker, apk inside it, and the
	// cloud CLIs distrobox.conf installs into it.
	box, err := g.run(`cd /tmp && sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 timeout 900 distrobox create --yes >/dev/null 2>&1 && sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 timeout 600 distrobox enter wolfi -- sh -c 'apk --version; aws --version; gcloud --version | head -1; test -s /usr/share/terminfo/x/xterm-ghostty && echo ghostty-terminfo' 2>&1 | tail -4`, "")
	r.add("wolfi container", err == nil && strings.Contains(box, "apk-tools") && strings.Contains(box, "aws-cli/2.") && strings.Contains(box, "Google Cloud SDK") && strings.Contains(box, "ghostty-terminfo"), oneLine(box)+errText(err))
	// Ghostty's TERM, which ncurses programs (top, nano, htop) refuse to start without.
	colors, err := g.run(`TERM=xterm-ghostty tput colors`, "")
	r.add("ghostty's terminfo", err == nil && strings.TrimSpace(colors) == "256", oneLine(colors)+errText(err))
	tools, err := g.run(`task --version && ifconfig lo | head -1 && unpigz --version 2>&1 | head -1`, "")
	r.add("command-line tools", err == nil && strings.Contains(tools, "3.54") && strings.Contains(tools, "lo:"), oneLine(tools)+errText(err))
	// Flathub is a static remote (usr/share/flatpak/remotes.d), added the first time flatpak
	// looks; Chrome is installed as a person does, when asked for.
	chrome, err := g.run(`timeout 900 flatpak install --system --noninteractive -y flathub com.google.Chrome >/dev/null 2>&1 && flatpak info --system com.google.Chrome | grep -m1 Ref:`, "")
	r.add("flathub", err == nil && strings.Contains(chrome, "com.google.Chrome"), oneLine(chrome)+errText(err))
	// libadwaita apps, the image's and Flathub's, take Orchis from ~/.config/gtk-4.0: real files
	// (construct theme), which a Flatpak reads through the image's read-only override. The marker
	// is the dark styles' media query, which only the Orchis fork's light gtk.css carries.
	gtk4, err := g.run(`f=/var/home/live/.config/gtk-4.0/gtk.css; test -f "$f" && ! test -L "$f" && sudo -u live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus flatpak run --command=sh com.google.Chrome -c 'grep -c "prefers-color-scheme" "$HOME/.config/gtk-4.0/gtk.css"' 2>&1`, "")
	r.add("gtk4 theme in flatpak", err == nil && strings.TrimSpace(lastLine(gtk4)) != "0", "Chrome's sandbox reads the copied gtk.css: "+lastLine(gtk4))
	// GTK 3 apps from Flathub - Chrome's own window - find the theme gtk-theme names in ~/.themes,
	// which construct theme fills and the same override shows them.
	gtk3, err := g.run(`t=$(sudo -u live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gsettings get org.gnome.desktop.interface gtk-theme | tr -d "'"); sudo -u live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus flatpak run --command=sh com.google.Chrome -c "test -s \"\$HOME/.themes/$t/gtk-3.0/gtk.css\" && echo \"$t\"" 2>&1`, "")
	r.add("gtk3 theme in flatpak", err == nil && strings.HasPrefix(strings.TrimSpace(lastLine(gtk3)), "Orchis-Construct-"), "Chrome's sandbox finds gtk-theme in ~/.themes: "+lastLine(gtk3))
	// The brand's wallpaper, light and dark: both files the session's background names are there.
	wall, err := g.run(`for k in picture-uri picture-uri-dark; do u=$(sudo -u live DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gsettings get org.gnome.desktop.background $k | tr -d "'"); f=${u#file://}; test -s "$f" && echo "$f"; done`, "")
	r.add("the brand's wallpaper", err == nil && strings.Count(wall, "/usr/share/backgrounds/construct/") == 2, oneLine(wall)+errText(err))

	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n != "" {
			want[n] = true
		}
	}
	// A name -only gives that nothing answers to would run nothing, and pass.
	known := map[string]bool{"views": true, "bench": true, "suspend": true}
	for _, a := range apps {
		known[a.id] = true
	}
	for _, p := range panels {
		known["panel:"+p] = true
	}
	for n := range want {
		if !known[n] {
			r.add("-only "+n, false, "no app, panel:<name>, views, bench or suspend is called that")
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
		r.shotLook(v, g, strings.TrimSuffix(a.id, ".desktop"))
		l.view(strings.TrimSuffix(a.id, ".desktop"))
		l.app(a.id, strings.TrimSuffix(a.id, ".desktop"))
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
		r.shotLook(v, g, "panel-"+p)
		l.view("panel-" + p)
	}
	_, _ = g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop")?.request_quit(); true`)
	if len(want) == 0 || want["views"] {
		r.gtkViews(v, g, l)
		r.shellViews(v, g, l)
		r.desktopViews(v, g, l)
		l.guestLook()
	}
	l.finish()
	if len(want) == 0 || want["bench"] {
		r.bench(g)
	}
	if len(want) == 0 || want["suspend"] {
		r.suspend(v, g)
	}
	r.compareNeedles(*needles)
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
// settle is how long the view takes to be what is shot, when not shellSettle.
type shellView struct {
	name, open, close, wake string
	settle                  time.Duration
}

// shellSettle is how long a view is given: menus open in under a second; a window (Settings)
// needs a few to draw.
const shellSettle = 4 * time.Second

// shellViews ends with the lock screen: its prompt is the unlock dialog the shell's AuthPrompt
// draws, which GNOME 51 restyled, and nothing after it in the run must find the session locked.
var shellViews = []shellView{
	{"overview", `Main.overview.show()`, `Main.overview.hide()`, "", 0},
	{"overview-search", `Main.overview.show(); Main.overview.searchEntry.set_text("se")`, `Main.overview.searchEntry.set_text(""); Main.overview.hide()`, "", 0},
	{"app-grid", `Main.overview.showApps()`, `Main.overview.hide()`, "", 0},
	{"quick-settings", `Main.panel.statusArea.quickSettings.menu.open()`, `Main.panel.statusArea.quickSettings.menu.close()`, "", 0},
	// The first quick toggle with the key focus, once the menu is open: its focus ring, against
	// the view above.
	{"quick-settings-focus",
		`const m = Main.panel.statusArea.quickSettings.menu; m.open(); GLib.timeout_add(GLib.PRIORITY_DEFAULT, 800, () => { find(m.box, 'quick-toggle')?.grab_key_focus(); return GLib.SOURCE_REMOVE; })`,
		`Main.panel.statusArea.quickSettings.menu.close()`, "", 0},
	// A toggle's menu expanded (the network's): a well in the menu, not a light slab.
	{"quick-settings-submenu",
		`const m = Main.panel.statusArea.quickSettings.menu; m.open(); GLib.timeout_add(GLib.PRIORITY_DEFAULT, 800, () => { lfAll(m.box, lfClass('quick-toggle-has-menu')).find(t => t.is_mapped() && t.menuEnabled)?.menu.open(true); return GLib.SOURCE_REMOVE; })`,
		`const m = Main.panel.statusArea.quickSettings.menu; lfAll(m.box, lfClass('quick-toggle-has-menu')).forEach(t => t.menu?.close(false)); m.close()`, "", 0},
	{"calendar", `Main.panel.statusArea.dateMenu.menu.open()`, `Main.panel.statusArea.dateMenu.menu.close()`, "", 0},
	// A day that is not today with the key focus: the ring is focus's alone (F1).
	{"calendar-focus",
		`const m = Main.panel.statusArea.dateMenu; m.menu.open(); GLib.timeout_add(GLib.PRIORITY_DEFAULT, 800, () => { (m._calendar._buttons ?? []).find(b => b.is_mapped() && b.reactive && !b.has_style_class_name('calendar-today') && !b.has_style_class_name('calendar-other-month'))?.grab_key_focus(); return GLib.SOURCE_REMOVE; })`,
		`Main.panel.statusArea.dateMenu.menu.close()`, "", 0},
	// The message list dense with notifications, once their banners have gone.
	{"notifications",
		`[["Backup finished", "312 files were copied to the external disk; the oldest copy was removed to make room."], ["Software updates", "A new version of Construct is ready: restart to use it."], ["Disk almost full", "Only 2.1 GB is left on /var. Empty the trash or remove what you no longer need."], ["Connected", "Wired connection established."]].forEach(([t, b]) => Main.notify(t, b)); GLib.timeout_add(GLib.PRIORITY_DEFAULT, 7000, () => { Main.panel.statusArea.dateMenu.menu.open(); return GLib.SOURCE_REMOVE; })`,
		`Main.panel.statusArea.dateMenu.menu.close(); Main.messageTray.getSources().forEach(s => s.destroy())`, "", 10 * time.Second},
	// libadwaita apps must follow the light/dark switch: Settings, opened in the light scheme.
	{"settings-light",
		`new imports.gi.Gio.Settings({schema_id: "org.gnome.desktop.interface"}).set_string("color-scheme", "default"); Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").activate()`,
		`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").request_quit(); new imports.gi.Gio.Settings({schema_id: "org.gnome.desktop.interface"}).set_string("color-scheme", "prefer-dark")`, "", 0},
	{"lock-screen", `Main.screenShield.lock(false)`, ``, "pointer", 0},
	{"lock-prompt", `Main.screenShield.showDialog()`, `Main.screenShield.deactivate(false)`, "key", 0},
}

// Each view opens and closes with lf.go's helpers at hand (find, lfAll), and its lf checks measure
// it while it is open.
func (r *report) shellViews(v *vm, g *guest, l *lf) {
	for _, s := range shellViews {
		_, err := g.eval(lfPrelude + s.open + "; true")
		r.add("view:"+s.name, err == nil, "screenshot shell-"+s.name+".png")
		settle := s.settle
		if settle == 0 {
			settle = shellSettle
		}
		time.Sleep(settle)
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
		r.shotLook(v, g, "shell-"+s.name)
		l.view("shell-" + s.name)
		if s.name == "lock-screen" {
			r.locked(g)
		}
		if s.close != "" {
			_, _ = g.eval(lfPrelude + s.close + "; true")
			time.Sleep(time.Second)
		}
	}
}

// asLive runs a command in the live user's session.
const asLive = `sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus `

// desktopViews are the desktop over each of the brand's wallpapers (E2: the bars' edge over every
// one), the desktop at 150%, and Settings in Spanish, whose strings run longer than English's.
func (r *report) desktopViews(v *vm, g *guest, l *lf) {
	walls, err := g.run(`ls /usr/share/backgrounds/construct/*.png`, "")
	if err != nil || strings.TrimSpace(walls) == "" {
		r.add("view:desktop wallpapers", false, "no wallpaper in /usr/share/backgrounds/construct"+errText(err))
	}
	for _, f := range strings.Fields(walls) {
		name := "desktop-" + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "construct-"), ".png")
		// Both keys, so the scheme in force shows it.
		_, err := g.run(asLive+`gsettings set org.gnome.desktop.background picture-uri 'file://`+f+`' && `+
			asLive+`gsettings set org.gnome.desktop.background picture-uri-dark 'file://`+f+`'`, "")
		r.add("view:"+name, err == nil, f+errText(err))
		time.Sleep(3 * time.Second)
		r.shotLook(v, g, name)
		l.view(name)
	}
	_, _ = g.run(asLive+`gsettings reset org.gnome.desktop.background picture-uri; `+asLive+`gsettings reset org.gnome.desktop.background picture-uri-dark`, "")
	time.Sleep(2 * time.Second)

	// 150%: mutter's native backend lays monitors out logically, so fractional scales are offered
	// without its experimental key; gdctl takes the supported scale nearest 1.5. Temporary: it is
	// not written to monitors.xml, and the run sets 1 back.
	monitor := `c=$(` + asLive + `gdctl show | grep -o 'Monitor [^ ]*' | head -1 | cut -d' ' -f2) && `
	out, err := g.run(monitor+asLive+`gdctl set --logical-monitor --primary --monitor "$c" --scale 1.5 2>&1 && `+asLive+`gdctl show 2>&1 | grep -m1 'Scale:'`, "")
	if err != nil {
		r.skip("view:"+lfScaledDesktop, "gdctl could not set 150%: "+oneLine(out)+errText(err))
	} else {
		time.Sleep(4 * time.Second)
		r.add("view:"+lfScaledDesktop, true, oneLine(out))
		r.shotLook(v, g, lfScaledDesktop)
		l.view(lfScaledDesktop)
		back, err := g.run(monitor+asLive+`gdctl set --logical-monitor --primary --monitor "$c" --scale 1 2>&1`, "")
		r.add("view:"+lfScaledDesktop+" restored", err == nil, "scale 1 again: "+oneLine(back)+errText(err))
		time.Sleep(3 * time.Second)
	}

	// Settings in Spanish: a process of its own, started with the language, once no instance is
	// left for the request to go to.
	_, _ = g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop")?.request_quit(); true`)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if n, err := g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").get_windows().length`); err == nil && n == "0" {
			break
		}
	}
	if _, err := g.eval(`GLib.spawn_command_line_async("env LANGUAGE=es LANG=es_ES.UTF-8 LC_ALL=es_ES.UTF-8 gnome-control-center power")`); err != nil {
		r.add("view:panel-power-es", false, err.Error())
		return
	}
	var title string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && title == ""; time.Sleep(time.Second) {
		t, err := g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop").get_windows()[0]?.get_title() ?? ""`)
		if err == nil {
			title = strings.Trim(t, `"`)
		}
	}
	time.Sleep(3 * time.Second)
	r.add("view:panel-power-es", title != "", "window "+title)
	r.shotLook(v, g, "panel-power-es")
	l.view("panel-power-es")
	_, _ = g.eval(`Shell.AppSystem.get_default().lookup_app("org.gnome.Settings.desktop")?.request_quit(); true`)
	time.Sleep(2 * time.Second)
}

// lockScreenRoles are the panel's own indicators the lock screen shows (gnome-shell's
// js/ui/panel.js PANEL_ITEM_IMPLEMENTATIONS, less what _hideIndicators hides): anything else the
// panel shows while locked is an extension's.
var lockScreenRoles = []string{"activities", "quickSettings", "dateMenu", "a11y", "keyboard", "dwellClick", "screenRecording", "screenSharing"}

// locked checks what the lock screen shows of the extensions. Tiling Shell and AppIndicator stay
// enabled there (session-modes unlock-dialog), so a lock does not tear them down and build them
// again, and each hides what it puts in the panel; nothing of an extension may show on it.
func (r *report) locked(g *guest) {
	state, err := g.eval(`String(Main.extensionManager.lookup("tilingshell@ferrarodomenico.com")?.state)`)
	// ExtensionState.ACTIVE
	r.add("locked: tilingshell stays enabled", err == nil && strings.Trim(state, `"`) == "1", "state "+state+errText(err))
	roles, _ := json.Marshal(lockScreenRoles)
	shown, err := g.eval(`JSON.stringify(Object.entries(Main.panel.statusArea).filter(([role, i]) => !` + string(roles) +
		`.includes(role) && i.container.visible && i.visible).map(([role]) => role))`)
	r.add("locked: no extension in the panel", err == nil && strings.Trim(shown, `"`) == "[]", "shown: "+shown+errText(err))
}

// keyringRoundTrip is a shell command that keeps a secret in user's login keyring and reads it
// back, printing "kept" when both work within a timeout - which a password prompt would outlast.
func keyringRoundTrip(user string, uid int) string {
	env := fmt.Sprintf("sudo -u %s env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus timeout 20 ", user, uid)
	return `echo kept | ` + env + `secret-tool store --label=harness harness probe 2>&1 && ` + env + `secret-tool lookup harness probe 2>&1`
}

// secureBoot checks the machine came up with Secure Boot on, in user mode - the keys systemd-boot
// enrolled from the ESP - and the kernel locked down for it.
func (r *report) secureBoot(g *guest) {
	out, _ := g.run(`bootctl status 2>/dev/null | grep -E 'Secure Boot:'; cat /sys/kernel/security/lockdown`, "")
	r.add("secure boot", strings.Contains(out, "Secure Boot: enabled (user)") && strings.Contains(out, "[integrity]"), oneLine(out))
}

// installerWindows counts the graphical installer's windows on the live session's screen.
const installerWindows = `global.get_window_actors().map(a => a.meta_window).filter(w => w.get_wm_class() === "io.github.construct_linux.Installer")`

// installer checks the live session offers the graphical installer at login on a stick, and
// nowhere else; its window is shot, measured (lf), and closed, so it covers nothing after.
func (r *report) installer(v *vm, g *guest, l *lf, stick bool) {
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
	// Its pages, walked as a person does (image/test/rootfs/usr/lib/construct-test).
	walked, err := g.run(`sudo -u live env XDG_RUNTIME_DIR=/run/user/1000 WAYLAND_DISPLAY=wayland-0 `+
		`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus /usr/lib/construct-test/walk-installer.py 2>&1`, "")
	r.add("installer's pages", err == nil && strings.Contains(walked, "walked"), oneLine(walked)+errText(err))
	if stick && strings.Trim(n, `"`) == "1" {
		time.Sleep(2 * time.Second)
		r.shotLook(v, g, "installer")
		l.view("installer")
		_, _ = g.eval(installerWindows + `.forEach(w => w.delete(global.get_current_time())); true`)
		time.Sleep(time.Second)
	}
}

// suspend sleeps the machine as a laptop's lid does and wakes it: it slept (the kernel counts a
// successful suspend), the session came back locked - /var's key stays in memory while it sleeps,
// and the lock screen is what keeps it - and the network is up again. Last: the session stays
// locked after it.
//
// It sleeps as the laptops Construct is for do: s2idle, modern standby, the one state they have.
// QEMU's S3 is no stand-in for them - its xHCI controller cannot save and restore its state
// (USBSTS SRE), the kernel resets it on resume, and the stick the live root is read from comes
// back offline. A key press wakes it: the PS/2 keyboard is made a wakeup source, as a laptop's is.
func (r *report) suspend(v *vm, g *guest) {
	before, _ := g.run(`cat /sys/power/suspend_stats/success`, "")
	_, err := g.run(`echo s2idle > /sys/power/mem_sleep && echo enabled > /sys/devices/platform/i8042/serio0/power/wakeup`, "")
	if err == nil {
		// --no-block: the job ends only once the machine is awake again, which this side does.
		_, err = g.run(`systemctl suspend --no-block`, "")
	}
	if err == nil {
		// Asleep once the guest stops answering.
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
			if _, perr := g.run("true", ""); perr != nil {
				break
			}
		}
		time.Sleep(3 * time.Second)
		err = v.key("shift")
	}
	if err == nil {
		err = g.wait(2 * time.Minute)
	}
	after, _ := g.run(`cat /sys/power/suspend_stats/success`, "")
	// What the machine said from the suspend on: what a resume broke is read here.
	resumed, _ := g.run(`journalctl -b -q --no-pager -o short-monotonic -p warning --since "-5min"`, "")
	_ = os.WriteFile(filepath.Join(r.dir, "resume.txt"), []byte(resumed), 0o644)
	slept := err == nil && strings.TrimSpace(after) != strings.TrimSpace(before)
	if !slept {
		// What logind and the kernel said, for the failure to carry its own reason.
		// The kernel's own account first: a suspend it tried and aborted counts as a failure, with
		// the device and the error that stopped it.
		why, _ := g.run(`cat /sys/power/mem_sleep; cd /sys/power/suspend_stats && echo "fail=$(cat fail) failed_suspend=$(cat failed_suspend) last_failed_dev=$(cat last_failed_dev) last_failed_errno=$(cat last_failed_errno)"; `+
			`journalctl -b -q -o short-monotonic -u systemd-logind -u systemd-suspend.service --since -3min | tail -8; systemd-inhibit --list --no-legend --no-pager | head -5`, "")
		r.add("suspend and resume", false, "suspends: "+oneLine(before)+" -> "+oneLine(after)+errText(err)+"; "+oneLine(why))
		return
	}
	r.add("suspend and resume", true, "suspends: "+oneLine(before)+" -> "+oneLine(after))
	locked, _ := g.eval(`Main.screenShield.locked`)
	r.add("locked after resume", locked == "true", "screenShield.locked: "+locked)
	// construct power's sleep hook wrote what the suspend cost.
	// systemd-sleep runs the post hooks after the machine answers again: waited for.
	hook, _ := g.run(`for i in $(seq 30); do m=$(journalctl -b -q -o cat MESSAGE_ID=8b1d2f6c3a4e4d0f9c7b5a2e1f6d3c4b | tail -1); [ -n "$m" ] && break; sleep 1; done; echo "$m"`, "")
	if !strings.HasPrefix(hook, "Slept ") {
		// The hook's own words, when it said something else.
		hook, _ = g.run(`journalctl -b -q -o cat -t systemd-sleep -t construct-power | tail -4`, "")
		r.add("the suspend is measured", false, oneLine(hook))
	} else {
		r.add("the suspend is measured", true, oneLine(hook))
	}
	net, _ := g.run(`for i in $(seq 30); do nmcli -t -f STATE general | grep -qx connected && break; sleep 1; done; nmcli -t -f STATE general`, "")
	r.add("network after resume", strings.TrimSpace(net) == "connected", oneLine(net))
}

// bench runs construct bench briefly: that it runs, measures both profiles and puts the machine's
// settings back - its numbers are a VM's, not a finding.
func (r *report) bench(g *guest) {
	before, _ := g.run(`sysctl -n vm.swappiness; swapon --show=NAME --noheadings`, "")
	out, err := g.run(`construct bench --rounds 1 --seconds 5 2>&1 | tail -6`, "")
	after, _ := g.run(`sysctl -n vm.swappiness; swapon --show=NAME --noheadings`, "")
	r.add("construct bench", err == nil && strings.Contains(out, "wakeup latency p99") && before == after, oneLine(out)+errText(err))
}
