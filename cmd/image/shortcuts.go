package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/qemu"
)

// shortcutChord is one key chord the shortcuts matrix presses in every target, as QEMU qcodes held
// together. setup is what the target is put through first, after its reset: "select" selects
// everything with the target's own select-all, "type" types d, "undo" types d and undoes it.
type shortcutChord struct {
	name  string
	keys  []string
	setup string
	// skip names the targets the chord is not pressed in, and why.
	skip map[string]string
}

// shortcutChords are the chords the Super layer is about, the physical Ctrl ones beside them for
// comparison, and the two that close or quit last: a target they close is started again.
var shortcutChords = []shortcutChord{
	{name: "Super+C", keys: []string{"meta_l", "c"}, setup: "select"},
	{name: "Super+V", keys: []string{"meta_l", "v"}},
	{name: "Super+X", keys: []string{"meta_l", "x"}, setup: "select"},
	{name: "Super+A", keys: []string{"meta_l", "a"}},
	{name: "Super+Z", keys: []string{"meta_l", "z"}, setup: "type"},
	{name: "Super+Shift+Z", keys: []string{"meta_l", "shift", "z"}, setup: "undo"},
	{name: "Super+S", keys: []string{"meta_l", "s"}},
	{name: "Super+F", keys: []string{"meta_l", "f"}},
	{name: "Super+T", keys: []string{"meta_l", "t"}},
	{name: "Super+Shift+T", keys: []string{"meta_l", "shift", "t"}},
	{name: "Super+N", keys: []string{"meta_l", "n"}},
	{name: "Super+R", keys: []string{"meta_l", "r"}},
	{name: "Super+K", keys: []string{"meta_l", "k"}},
	{name: "Super+B", keys: []string{"meta_l", "b"}},
	{name: "Super+O", keys: []string{"meta_l", "o"}},
	{name: "Super+,", keys: []string{"meta_l", "comma"}},
	{name: "Super+=", keys: []string{"meta_l", "equal"}},
	{name: "Super+-", keys: []string{"meta_l", "minus"}},
	{name: "Ctrl+C", keys: []string{"ctrl", "c"}, setup: "select"},
	{name: "Ctrl+V", keys: []string{"ctrl", "v"}},
	{name: "Ctrl+Shift+C", keys: []string{"ctrl", "shift", "c"}, setup: "select",
		skip: map[string]string{"Firefox": "opens the inspector, which keeps the focus from every chord after"}},
	{name: "Ctrl+Shift+V", keys: []string{"ctrl", "shift", "v"}},
	{name: "Super+W", keys: []string{"meta_l", "w"}},
	{name: "Super+Q", keys: []string{"meta_l", "q"}},
}

// shortcutTarget is an application the matrix presses chords in, with a probe that reports what
// reached it: image/test's key-probe.py (GTK 4) and key-probe.html (a browser) in their window
// titles, tty-probe.py (inside a terminal) in a file of the bytes the terminal sent.
type shortcutTarget struct {
	name  string
	app   string // the desktop id it is quit by, after
	spawn string
	mark  string // in its probe window's title
	out   string // the terminal probe's file
}

const ttyProbe = "/usr/lib/construct-test/tty-probe.py"

var shortcutTargets = []shortcutTarget{
	{name: "GTK 4", spawn: "/usr/lib/construct-test/key-probe.py", mark: `"probe": "gtk"`},
	{name: "Ghostty", app: "com.mitchellh.ghostty.desktop", mark: "construct-tty-ghostty",
		out:   "/run/user/1000/construct-test/tty-ghostty.txt",
		spawn: "ghostty -e " + ttyProbe + " /run/user/1000/construct-test/tty-ghostty.txt construct-tty-ghostty"},
	{name: "Ptyxis", app: "org.gnome.Ptyxis.desktop", mark: "construct-tty-ptyxis",
		out:   "/run/user/1000/construct-test/tty-ptyxis.txt",
		spawn: "ptyxis --new-window -x '" + ttyProbe + " /run/user/1000/construct-test/tty-ptyxis.txt construct-tty-ptyxis'"},
	{name: "Firefox", app: "firefox.desktop", mark: `"probe":"web"`,
		spawn: "firefox --new-window file:///usr/lib/construct-test/key-probe.html"},
}

// shortcutSeen is what the shell saw after a chord, beside what the probe reported.
type shortcutSeen struct {
	Alive    bool
	Title    string
	Focus    string
	Target   bool `json:"focusTarget"`
	Windows  []int
	Overview bool
	Clip     string
	Prim     string
}

// shortcutCell is one chord in one target: what the target was sent and what came of it.
type shortcutCell struct {
	Chord, Target, Result string
}

// The clipboards' contents before each chord: a paste of one is told from the other.
const (
	seedClip = "CLIP"
	seedPrim = "PRIM"
	ttyMark  = "\x1b[20~" // F9, the mark between chords in a terminal's bytes
)

// shortcuts measures what each Super and Ctrl chord does in each kind of application the image
// carries - GTK 4, two terminals (Ghostty, and VTE's Ptyxis) and a browser - through everything a
// key goes through: QEMU's keyboard, the kernel, keyd, Mutter and the toolkit. It is a
// measurement, not a check: shortcuts.txt is the matrix, a row per chord with what keyd sends and
// what GNOME binds it to, and shortcuts.json the same cells for a later run to compare.
func (r *report) shortcuts(v *qemu.VM, g *guest) {
	keyd, _ := g.run(`cat /etc/keyd/default.conf`, "")
	sends := keydMeta(keyd)
	bound, _ := g.run(gnomeBindingsScript, "")
	binds := superBindings(bound)
	_, _ = g.run(asLive+`mkdir -p /run/user/1000/construct-test; rm -f /run/user/1000/construct-test/tty-*.txt`, "")

	var cells []shortcutCell
	launched := map[string]string{}
	for _, t := range shortcutTargets {
		seq, err := r.startProbe(g, t)
		if err != nil {
			launched[t.name] = err.Error()
			continue
		}
		for _, c := range shortcutChords {
			if why, ok := c.skip[t.name]; ok {
				cells = append(cells, shortcutCell{c.name, t.name, "not pressed: " + why})
				continue
			}
			if seq == 0 {
				if seq, err = r.startProbe(g, t); err != nil {
					cells = append(cells, shortcutCell{c.name, t.name, "the probe did not start again: " + err.Error()})
					continue
				}
			}
			res, alive := pressChord(v, g, t, c, seq)
			cells = append(cells, shortcutCell{c.name, t.name, res})
			if !alive {
				seq = 0
			}
		}
		stopProbe(g, t)
	}

	var b strings.Builder
	cols := []string{"chord", "keyd sends", "GNOME binds"}
	for _, t := range shortcutTargets {
		cols = append(cols, t.name)
	}
	fmt.Fprintf(&b, "| %s |\n|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
	for _, c := range shortcutChords {
		row := []string{c.name, orDash(sends[chordKey(c.keys)]), orDash(binds[chordKey(c.keys)])}
		for _, t := range shortcutTargets {
			cell := "-"
			for _, x := range cells {
				if x.Chord == c.name && x.Target == t.name {
					cell = x.Result
				}
			}
			row = append(row, strings.ReplaceAll(cell, "|", `\|`))
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(row, " | "))
	}
	for name, why := range launched {
		fmt.Fprintf(&b, "\n%s did not start: %s\n", name, why)
	}
	_ = os.WriteFile(filepath.Join(r.dir, "shortcuts.txt"), []byte(b.String()), 0o644)
	js, _ := json.MarshalIndent(cells, "", "  ")
	_ = os.WriteFile(filepath.Join(r.dir, "shortcuts.json"), js, 0o644)
	r.add("shortcuts matrix", len(launched) == 0, fmt.Sprintf("%d chords in %d targets: shortcuts.txt; not started: %v",
		len(shortcutChords), len(shortcutTargets)-len(launched), launched))
}

// startProbe starts t's probe and answers its window's stable sequence, which outlives a title
// the chords change (a browser's new tab, a terminal's).
func (r *report) startProbe(g *guest, t shortcutTarget) (int, error) {
	if _, err := g.eval(fmt.Sprintf(`GLib.spawn_command_line_async(%q); true`, t.spawn)); err != nil {
		return 0, err
	}
	var seq int
	ok := until(90*time.Second, time.Second, func() bool {
		_ = g.evalJSON(fmt.Sprintf(`return global.get_window_actors().map(a => a.meta_window).find(w => (w.get_title() ?? '').includes(%q))?.get_stable_sequence() ?? 0;`, t.mark), &seq)
		return seq != 0
	})
	if !ok {
		return 0, fmt.Errorf("no window titled %q within 90 s", t.mark)
	}
	if t.out != "" && !until(10*time.Second, 500*time.Millisecond, func() bool {
		_, err := g.run(`test -s `+t.out, "")
		return err == nil
	}) {
		return 0, fmt.Errorf("%s never wrote %s", ttyProbe, t.out)
	}
	return seq, nil
}

func stopProbe(g *guest, t shortcutTarget) {
	_, _ = g.run(`pkill -u live -f 'construct-test/(key|tty)-probe' ; true`, "")
	if t.app != "" {
		g.quitApp(t.app)
	}
	time.Sleep(2 * time.Second)
}

// pressChord resets the probe, puts it through c's setup with the clipboards seeded, presses c
// and describes what came of it; then it undoes what the chord opened - windows, tabs, the
// overview - so that the next chord starts from the probe alone. It answers whether the probe's
// window is still there.
func pressChord(v *qemu.VM, g *guest, t shortcutTarget, c shortcutChord, seq int) (string, bool) {
	key := func(keys ...string) {
		_ = v.Key(keys...)
		time.Sleep(350 * time.Millisecond)
	}
	if !until(10*time.Second, 500*time.Millisecond, func() bool {
		var has bool
		_ = g.evalJSON(fmt.Sprintf(`const w = global.get_window_actors().map(a => a.meta_window).find(w => w.get_stable_sequence() === %d); if (!w) return false; Main.activateWindow(w); return w.has_focus();`, seq), &has)
		return has
	}) {
		return "the probe's window never had the focus", true
	}
	_, _ = g.eval(`const St = imports.gi.St; const c = St.Clipboard.get_default(); ` +
		`c.set_text(St.ClipboardType.CLIPBOARD, "` + seedClip + `"); c.set_text(St.ClipboardType.PRIMARY, "` + seedPrim + `"); true`)
	time.Sleep(300 * time.Millisecond)
	key("esc")
	tty := t.out != ""
	// The reset, or a terminal's mark, goes before the setup: a GUI probe's reset clears what the
	// setup did, and a terminal (Ghostty) drops its selection at the next key it sends.
	key("f9")
	base := "abc"
	switch c.setup {
	case "select":
		if tty {
			key("ctrl", "shift", "a")
		} else {
			key("ctrl", "a")
		}
	case "type", "undo":
		if !tty {
			key("d")
			base = "abcd"
			if c.setup == "undo" {
				key("ctrl", "z")
				base = "abc"
			}
		}
	}
	shells := liveShells(g)
	var before shortcutSeen
	_ = g.evalJSON(seenJS(seq), &before)

	key(c.keys...)
	time.Sleep(600 * time.Millisecond)

	var after shortcutSeen
	_ = g.evalJSON(seenJS(seq), &after)
	var parts []string
	if tty {
		parts = append(parts, ttyResult(g, t.out))
	} else {
		parts = append(parts, guiResult(after.Title, base))
	}
	if c.setup == "select" && before.Prim == seedPrim {
		parts = append(parts, "the setup's select-all selected nothing")
	}
	if after.Clip != seedClip {
		parts = append(parts, fmt.Sprintf("clipboard %q", after.Clip))
	}
	if after.Overview {
		parts = append(parts, "the overview opened")
		_, _ = g.eval(`Main.overview.hide(); true`)
	}
	if !after.Alive {
		parts = append(parts, "its window closed")
	} else if !strings.Contains(after.Title, t.mark) {
		parts = append(parts, fmt.Sprintf("its window now %q", after.Title))
	}
	if after.Alive && !after.Target && !after.Overview {
		parts = append(parts, fmt.Sprintf("the focus went to %q", after.Focus))
	}
	var opened []int
	for _, w := range after.Windows {
		if !slices.Contains(before.Windows, w) {
			opened = append(opened, w)
		}
	}
	if len(opened) > 0 {
		parts = append(parts, fmt.Sprintf("%d window(s) opened", len(opened)))
		ids, _ := json.Marshal(opened)
		_, _ = g.eval(`global.get_window_actors().map(a => a.meta_window).filter(w => ` + string(ids) +
			`.includes(w.get_stable_sequence())).forEach(w => w.delete(global.get_current_time())); true`)
		time.Sleep(time.Second)
	}
	if n := liveShells(g) - shells; n > 0 {
		parts = append(parts, fmt.Sprintf("%d shell(s) started (a tab or window)", n))
		for range n {
			// An interactive bash ignores SIGTERM; a hangup is what closing its tab sends.
			_, _ = g.run(`pkill -HUP -n -u live -x bash; true`, "")
		}
		time.Sleep(time.Second)
	}
	// A tab the chord opened or reopened is closed by the application's own close-tab chord, which
	// keyd does not touch, until the probe's is the window's again: Ptyxis keeps a tab whose shell
	// has gone.
	closeTab := []string{"ctrl", "w"}
	if tty {
		closeTab = []string{"ctrl", "shift", "w"}
	}
	for range 3 {
		var now shortcutSeen
		_ = g.evalJSON(seenJS(seq), &now)
		if !now.Alive || strings.Contains(now.Title, t.mark) {
			break
		}
		key(closeTab...)
		time.Sleep(time.Second)
	}
	var end shortcutSeen
	_ = g.evalJSON(seenJS(seq), &end)
	return strings.Join(parts, "; "), end.Alive
}

func seenJS(seq int) string {
	return fmt.Sprintf(`const St = imports.gi.St; const c = St.Clipboard.get_default();
		const get = t => new Promise(r => c.get_text(t, (_, s) => r(s ?? '')));
		const ws = global.get_window_actors().map(a => a.meta_window);
		const w = ws.find(w => w.get_stable_sequence() === %d);
		const f = global.display.focus_window;
		return {alive: !!w, title: w?.get_title() ?? '', focus: f?.get_title() ?? '', focusTarget: !!w && w.has_focus(),
			windows: ws.map(w => w.get_stable_sequence()), overview: Main.overview.visible,
			clip: await get(St.ClipboardType.CLIPBOARD), prim: await get(St.ClipboardType.PRIMARY)};`, seq)
}

// liveShells counts the live user's interactive shells: a terminal's new tab or window starts one.
func liveShells(g *guest) int {
	out, _ := g.run(`pgrep -c -u live -x bash`, "")
	var n int
	_, _ = fmt.Sscan(strings.TrimSpace(out), &n)
	return n
}

// guiResult reads a GUI probe's title: the keys that reached the application and what its field
// holds, against base, what it held before the chord.
func guiResult(title, base string) string {
	i, j := strings.Index(title, "{"), strings.LastIndex(title, "}")
	if i < 0 || j < i {
		return "the probe's page or field is not in front"
	}
	var p struct {
		V string
		K []string
		R int
	}
	if err := json.Unmarshal([]byte(title[i:j+1]), &p); err != nil {
		return "the probe's title did not read: " + err.Error()
	}
	got := "nothing reached it"
	if len(p.K) > 0 {
		got = "got " + strings.Join(p.K, " ")
	}
	switch p.V {
	case base:
	case base + seedClip:
		got += "; pasted CLIPBOARD"
	case base + seedPrim:
		got += "; pasted PRIMARY"
	default:
		got += fmt.Sprintf("; field %q", p.V)
	}
	if p.R > 1 {
		got += fmt.Sprintf("; page loaded %d times", p.R)
	}
	return got
}

// ttyResult reads what a terminal sent its program after the last mark.
func ttyResult(g *guest, out string) string {
	raw, err := g.run(`cat `+out, "")
	if err != nil {
		return "the terminal probe's file: " + oneLine(raw)
	}
	var b []byte
	for _, line := range strings.Split(raw, "\n") {
		if x, err := hex.DecodeString(strings.TrimSpace(line)); err == nil {
			b = append(b, x...)
		}
	}
	i := bytes.LastIndex(b, []byte(ttyMark))
	if i < 0 {
		return "no mark in the terminal's bytes"
	}
	if s := ttyBytes(b[i+len(ttyMark):]); s != "" {
		return "sent " + s
	}
	return "nothing reached the program"
}

// ttyBytes spells out what a terminal sent: a bracketed paste with its text, control characters
// as ^X, escape sequences as ESC and what follows.
func ttyBytes(b []byte) string {
	var parts []string
	for len(b) > 0 {
		switch {
		case bytes.HasPrefix(b, []byte("\x1b[200~")):
			rest := b[6:]
			end := bytes.Index(rest, []byte("\x1b[201~"))
			if end < 0 {
				end = len(rest)
			}
			text := string(rest[:end])
			switch text {
			case seedClip:
				parts = append(parts, "paste of CLIPBOARD")
			case seedPrim:
				parts = append(parts, "paste of PRIMARY")
			default:
				parts = append(parts, fmt.Sprintf("paste %q", text))
			}
			b = rest[min(end+6, len(rest)):]
		case b[0] == 0x1b:
			n := 1
			if len(b) > 1 && (b[1] == '[' || b[1] == 'O') {
				n = 2
				for n < len(b) && (b[n] < 0x40 || b[n] > 0x7e) {
					n++
				}
				n = min(n+1, len(b))
			}
			parts = append(parts, "ESC"+string(b[1:n]))
			b = b[n:]
		case b[0] < 0x20:
			parts = append(parts, "^"+string(rune(b[0]+'@')))
			b = b[1:]
		case b[0] == 0x7f:
			parts = append(parts, "DEL")
			b = b[1:]
		default:
			n := 0
			for n < len(b) && b[n] >= 0x20 && b[n] != 0x7f {
				n++
			}
			parts = append(parts, fmt.Sprintf("%q", b[:n]))
			b = b[n:]
		}
	}
	return strings.Join(parts, " ")
}

// chordKey is a chord's name in one spelling for qcodes and GSettings accelerators alike:
// modifiers in a fixed order, then the key.
func chordKey(keys []string) string {
	mods := map[string]bool{}
	key := ""
	for _, k := range keys {
		switch strings.ToLower(k) {
		case "ctrl", "control", "primary", "control_l":
			mods["ctrl"] = true
		case "alt", "alt_l":
			mods["alt"] = true
		case "shift", "shift_l":
			mods["shift"] = true
		case "meta_l", "super", "super_l", "meta":
			mods["super"] = true
		default:
			key = strings.ToLower(k)
		}
	}
	var out []string
	for _, m := range []string{"ctrl", "alt", "shift", "super"} {
		if mods[m] {
			out = append(out, m)
		}
	}
	return strings.Join(append(out, key), "+")
}

// gnomeBindingsScript lists every keybinding GSettings holds with Super in it: the shell's,
// Mutter's, the settings daemon's and each installed extension's, as the live user has them.
const gnomeBindingsScript = asLive + `sh -c 'for s in org.gnome.desktop.wm.keybindings org.gnome.shell.keybindings org.gnome.mutter.keybindings org.gnome.mutter.wayland.keybindings org.gnome.settings-daemon.plugins.media-keys; do gsettings list-recursively $s; done; for d in /usr/share/gnome-shell/extensions/*/schemas; do for s in $(gsettings --schemadir $d list-schemas); do gsettings --schemadir $d list-recursively $s; done; done' 2>/dev/null | grep -i '<super>'`

var (
	accelRe = regexp.MustCompile(`'([^']*)'`)
	modRe   = regexp.MustCompile(`<([^>]+)>`)
)

// superBindings maps each chord with Super that GSettings binds to "schema key", from
// gnomeBindingsScript's lines.
func superBindings(lines string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(lines, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		for _, a := range accelRe.FindAllStringSubmatch(line, -1) {
			accel := a[1]
			if !strings.Contains(strings.ToLower(accel), "<super>") {
				continue
			}
			var keys []string
			for _, mod := range modRe.FindAllStringSubmatch(accel, -1) {
				keys = append(keys, mod[1])
			}
			keys = append(keys, modRe.ReplaceAllString(accel, ""))
			k := chordKey(keys)
			name := strings.TrimPrefix(f[0], "org.gnome.") + " " + f[1]
			if m[k] != "" {
				name = m[k] + ", " + name
			}
			m[k] = name
		}
	}
	return m
}

// keydMeta maps each chord with Super that etc/keyd/default.conf's [meta] layer rebinds to what
// it sends instead.
func keydMeta(conf string) map[string]string {
	m := map[string]string{}
	in := false
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == "[meta]"
			continue
		}
		k, val, ok := strings.Cut(line, "=")
		if !in || !ok || strings.HasPrefix(line, "#") {
			continue
		}
		m[chordKey([]string{"super", strings.TrimSpace(k)})] = strings.TrimSpace(val)
	}
	return m
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
