package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The type specimen and its rendering matrix: the same text, drawn by GTK 4 under each way of
// rendering text the desktop could ship, shot at the screen's density and measured (typemeasure.go),
// so what makes text less sharp than macOS's is a number and not an impression. The specimen is a
// view of its own (type-specimen, a needle); the matrix is _output/test/type.html and type.json.

//go:embed type/specimen.js
var specimenJS string

// typeDir is where the specimen runs in the guest: the shell starts it, as the live user.
const typeDir = "/tmp/construct-type"

// stemDarkening is FreeType's stem darkening, on for the drivers that have it: the auto-hinter
// (autofitter) - which draws Adwaita Sans, a variable TrueType font with no instructions, at
// every hinting but none, and Adwaita Mono at slight - and the Adobe engine of CFF fonts.
// FreeType reads FREETYPE_PROPERTIES once per process, so the specimen gets it alone.
const stemDarkening = "cff:no-stem-darkening=0 autofitter:no-stem-darkening=0"

// typeSetting is one way of rendering text: the GSettings keys GTK and the shell read (empty: the
// image's own value), GtkSettings' gtk-hint-font-metrics, and FreeType's stem darkening. Same
// names the setting GTK 4.24's source says draws it identically (gtk/gtkwidget.c,
// gtk_widget_update_pango_context: automatic is gray, slight, metrics off, whatever the keys say;
// gtk/gtksettings.c: antialiasing is on or off, never subpixel; FreeType darkens stems only in
// light mode) - the run measures whether it does.
type typeSetting struct {
	Name      string `json:"name"`
	Rendering string `json:"font_rendering,omitempty"`
	Hinting   string `json:"font_hinting,omitempty"`
	Antialias string `json:"font_antialiasing,omitempty"`
	Metrics   bool   `json:"hint_font_metrics,omitempty"`
	Darken    bool   `json:"stem_darkening,omitempty"`
	Same      string `json:"same_as,omitempty"`
	Why       string `json:"why"`
}

var typeSettings = []typeSetting{
	{Name: "default", Why: "the image as it ships: GNOME's keys (automatic, slight, grayscale)"},
	{Name: "auto-full-rgba", Rendering: "automatic", Hinting: "full", Antialias: "rgba", Same: "default",
		Why: "automatic ignores the hinting and antialiasing keys"},
	{Name: "auto-none", Rendering: "automatic", Hinting: "none", Antialias: "grayscale", Same: "default",
		Why: "automatic ignores hinting none too"},
	{Name: "manual-none", Rendering: "manual", Hinting: "none", Antialias: "grayscale",
		Why: "no hinting: glyphs at quarter-pixel positions both ways, no snapping"},
	{Name: "manual-slight", Rendering: "manual", Hinting: "slight", Antialias: "grayscale", Same: "default",
		Why: "what automatic picks, asked for by hand"},
	{Name: "manual-full", Rendering: "manual", Hinting: "full", Antialias: "grayscale",
		Why: "full hinting: the auto-hinter in normal mode, stems snapped both ways"},
	{Name: "manual-full-rgba", Rendering: "manual", Hinting: "full", Antialias: "rgba", Same: "manual-full",
		Why: "GTK 4 has no subpixel antialiasing: rgba is drawn gray"},
	{Name: "manual-slight-metrics", Rendering: "manual", Hinting: "slight", Antialias: "grayscale", Metrics: true,
		Why: "slight with metrics hinted: advances and positions rounded to whole pixels"},
	{Name: "manual-full-metrics", Rendering: "manual", Hinting: "full", Antialias: "grayscale", Metrics: true,
		Why: "the crispest GTK 4 draws: full hinting, whole-pixel metrics"},
	{Name: "darken", Darken: true,
		Why: "the image's rendering with FreeType's stem darkening: slight is light mode, where it applies"},
	{Name: "manual-none-darken", Rendering: "manual", Hinting: "none", Antialias: "grayscale", Darken: true, Same: "manual-none",
		Why: "no hinting is no auto-hinter: nothing to darken"},
	{Name: "manual-full-darken", Rendering: "manual", Hinting: "full", Antialias: "grayscale", Darken: true, Same: "manual-full",
		Why: "the auto-hinter darkens only in light mode, not full's normal one"},
}

// gtkSeen is what the specimen's GtkSettings resolved: the run measures what GTK was told only if
// GTK heard it.
type gtkSeen struct {
	FontRendering   string `json:"font_rendering"`
	XftHinting      int    `json:"xft_hinting"`
	XftHintstyle    string `json:"xft_hintstyle"`
	XftAntialias    int    `json:"xft_antialias"`
	XftRGBA         string `json:"xft_rgba"`
	HintFontMetrics bool   `json:"hint_font_metrics"`
	XftDPI          int    `json:"xft_dpi"`
}

// expectedGtk is what GTK's Wayland backend makes of the keys (gdk/wayland/gdksettings-wayland.c,
// update_xft_settings): hinting none is no hinting, the others a hint style; antialiasing none is
// off, grayscale on with no subpixel order, rgba on with font-rgba-order's.
func expectedGtk(rendering, hinting, antialias, order string, metrics bool) gtkSeen {
	e := gtkSeen{FontRendering: rendering, XftHinting: 1, XftHintstyle: "hint" + hinting, XftAntialias: 1, XftRGBA: "none", HintFontMetrics: metrics}
	if hinting == "none" {
		e.XftHinting = 0
	}
	switch antialias {
	case "none":
		e.XftAntialias = 0
	case "rgba":
		e.XftRGBA = order
	}
	return e
}

// matches is whether GTK saw e; the DPI is the text scaling's, not a rendering setting.
func (s gtkSeen) matches(e gtkSeen) bool {
	s.XftDPI = 0
	return s == e
}

// specimenItem is one line of the specimen as it reported it: where, in the window, and what.
type specimenItem struct {
	Theme  string  `json:"theme"`
	FG     string  `json:"fg"`
	BG     string  `json:"bg"`
	Family string  `json:"family"`
	Weight int     `json:"weight"`
	Size   int     `json:"size"`
	Kind   string  `json:"kind"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
}

type specimenRun struct {
	Fullscreen bool              `json:"fullscreen"`
	Surface    []float64         `json:"surface"`
	Env        string            `json:"env"`
	Settings   gtkSeen           `json:"settings"`
	Fonts      map[string]string `json:"fonts"`
	Items      []specimenItem    `json:"items"`
}

// typeRow is one size, weight, family and theme under one setting, measured.
type typeRow struct {
	Setting string     `json:"setting"`
	Theme   string     `json:"theme"`
	Family  string     `json:"family"`
	Weight  int        `json:"weight"`
	Size    int        `json:"size"`
	Stems   stemStats  `json:"stems"`
	Align   alignStats `json:"align"`
	Crop    string     `json:"crop"`
}

// typeShell is the shell's own text under a setting, measured: St draws it, not GTK.
type typeShell struct {
	Setting string    `json:"setting"`
	Item    string    `json:"item"`
	Stems   stemStats `json:"stems"`
	Crop    string    `json:"crop"`
}

// typeSame is a setting against the one GTK's source says draws the same: how many of the
// specimen's pixels differ at all.
type typeSame struct {
	Setting string `json:"setting"`
	Same    string `json:"same_as"`
	Differ  int    `json:"pixels_differ"`
}

type typeReport struct {
	Screen   string            `json:"screen"`
	Scale    int               `json:"scale"`
	Packages string            `json:"packages"`
	Settings []typeSetting     `json:"settings"`
	Seen     map[string]any    `json:"seen"`
	Fonts    map[string]string `json:"fonts"`
	Rows     []typeRow         `json:"rows"`
	Shell    []typeShell       `json:"shell"`
	Same     []typeSame        `json:"same"`
}

// typeKeys are the org.gnome.desktop.interface keys a setting changes.
var typeKeys = []string{"font-rendering", "font-hinting", "font-antialiasing"}

func (s typeSetting) values() []string { return []string{s.Rendering, s.Hinting, s.Antialias} }

// typeMatrix takes the specimen under every typeSetting, measures it, and writes type.json and
// type.html; the image's own rendering is the type-specimen view.
func (r *report) typeMatrix(v *vm, g *guest, l *lf) {
	dir := filepath.Join(r.dir, "type")
	if err := os.MkdirAll(filepath.Join(dir, "crops"), 0o755); err != nil {
		r.add("type", false, err.Error())
		return
	}
	if out, err := g.run(`install -d -m 0755 -o live -g live `+typeDir+` && cat > `+typeDir+`/specimen.js && chmod 0644 `+typeDir+`/specimen.js`, specimenJS); err != nil {
		r.add("type", false, "putting the specimen in the guest: "+oneLine(out)+errText(err))
		return
	}
	saved := map[string]string{}
	for _, k := range append(typeKeys, "font-rgba-order") {
		val, _ := g.run(asLive+`gsettings get org.gnome.desktop.interface `+k, "")
		saved[k] = strings.TrimSpace(val)
	}
	defer func() {
		g.stopSpecimen()
		for _, k := range typeKeys {
			_, _ = g.run(asLive+`gsettings set org.gnome.desktop.interface `+k+` "`+saved[k]+`"`, "")
		}
	}()
	pkgs, _ := g.run(`awk '/^P:/{p=substr($0,3)} /^V:/{v=substr($0,3); if (p ~ /^(gtk-4|libadwaita|pango|cairo|freetype|harfbuzz|fontconfig|gjs|mutter|gnome-shell|adwaita-fonts-sans|adwaita-fonts-mono)$/) printf "%s %s; ", p, v}' /usr/lib/apk/db/installed`, "")
	_, _ = g.eval(`Main.overview.hide(); Main.panel.statusArea.dateMenu.menu.close(); Main.panel.statusArea.quickSettings.menu.close(); true`)

	rep := typeReport{Scale: l.k, Packages: strings.TrimSpace(pkgs), Settings: typeSettings, Seen: map[string]any{}}
	shots := map[string]image.Image{}
	var frame lfRect
	shellDone := map[string]bool{}
	for _, s := range typeSettings {
		eff, err := g.applyTypeSetting(s, saved)
		if err != nil {
			r.add("type: "+s.Name, false, err.Error())
			continue
		}
		run, f, err := g.runSpecimen(s)
		if err != nil {
			r.add("type: "+s.Name, false, err.Error())
			continue
		}
		frame = f
		rendering, hinting, antialias := eff[0], eff[1], eff[2]
		want := expectedGtk(rendering, hinting, antialias, strings.Trim(saved["font-rgba-order"], "'"), s.Metrics)
		wantEnv := ""
		if s.Darken {
			wantEnv = stemDarkening
		}
		seen := run.Settings.matches(want) && run.Env == wantEnv && run.Fullscreen
		rep.Seen[s.Name] = run.Settings
		rep.Fonts = run.Fonts
		name := "type/" + s.Name
		if s.Name == "default" {
			// The image's own rendering is a view, compared with its needle like any other.
			name = "type-specimen"
			r.shotLook(v, g, name)
		} else {
			r.shot(v, name)
		}
		img, err := readPNG(filepath.Join(r.dir, name+".png"))
		if err != nil {
			r.add("type: "+s.Name, false, err.Error())
			continue
		}
		k, ok := scaleOf(img.Bounds().Dx(), l.stageW)
		if !ok {
			r.add("type: "+s.Name, false, fmt.Sprintf("%d px wide over a %d px stage", img.Bounds().Dx(), l.stageW))
			continue
		}
		rep.Scale = k
		rep.Screen = fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy())
		shots[s.Name] = img
		rows, err := measureSpecimen(img, run, frame, k, s.Name, dir)
		rep.Rows = append(rep.Rows, rows...)
		r.add("type: "+s.Name, seen && err == nil && len(rows) > 0,
			fmt.Sprintf("%d lines measured; GTK saw %+v, want %+v; FREETYPE_PROPERTIES %q, want %q; fullscreen %v%s",
				len(rows), run.Settings, want, run.Env, wantEnv, run.Fullscreen, errText(err)))
		g.stopSpecimen()
		// The shell draws its text with the keys too, and not GTK's metrics or a process's
		// FreeType: shot once for each combination of the keys.
		key := strings.Join(eff, ",")
		if !shellDone[key] {
			shellDone[key] = true
			items, err := r.shellType(v, g, s.Name, k, l.c("dark-text"), dir)
			rep.Shell = append(rep.Shell, items...)
			if err != nil {
				r.add("type: shell "+s.Name, false, err.Error())
			}
		}
	}
	for _, s := range typeSettings {
		a, b := shots[s.Name], shots[s.Same]
		if s.Same == "" || a == nil || b == nil {
			continue
		}
		rep.Same = append(rep.Same, typeSame{Setting: s.Name, Same: s.Same, Differ: pixelsDiffer(a, b, frame.image(), scaleIn(a, l))})
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.dir, "type.json"), b, 0o644)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(r.dir, "type.html"), []byte(typePage(rep)), 0o644)
	}
	r.add("type: matrix", err == nil && len(rep.Rows) > 0, fmt.Sprintf("%d settings, %d lines measured: type.html%s", len(typeSettings), len(rep.Rows), errText(err)))
}

// scaleIn is the scale of img over the stage.
func scaleIn(img image.Image, l *lf) int {
	s, ok := scaleOf(img.Bounds().Dx(), l.stageW)
	if !ok {
		return 1
	}
	return s
}

// applyTypeSetting sets s's keys - the image's own where s leaves one empty - and answers the
// values in force, unquoted.
func (g *guest) applyTypeSetting(s typeSetting, saved map[string]string) ([]string, error) {
	var eff []string
	for i, k := range typeKeys {
		v := s.values()[i]
		cmd := asLive + `gsettings set org.gnome.desktop.interface ` + k + ` "` + saved[k] + `"`
		if v != "" {
			cmd = asLive + `gsettings set org.gnome.desktop.interface ` + k + ` '` + v + `'`
		}
		out, err := g.run(cmd+` && `+asLive+`gsettings get org.gnome.desktop.interface `+k, "")
		if err != nil {
			return nil, fmt.Errorf("gsettings %s: %w: %s", k, err, oneLine(out))
		}
		eff = append(eff, strings.Trim(strings.TrimSpace(out), "'"))
	}
	// The settings portal tells GTK of the change; a GTK started before it would read the old.
	time.Sleep(time.Second)
	return eff, nil
}

// specimenWindow is the specimen's window on the stage.
const specimenWindow = `global.get_window_actors().map(a => a.meta_window).find(w => w.get_wm_class() === 'io.github.construct_linux.TypeSpecimen')`

// runSpecimen starts the specimen from the shell, in the session's environment, and waits for it
// to say where its lines are; it answers that and its window's frame.
func (g *guest) runSpecimen(s typeSetting) (specimenRun, lfRect, error) {
	var run specimenRun
	g.stopSpecimen()
	_, _ = g.run(`rm -f `+typeDir+`/specimen.json`, "")
	env := "env -u FREETYPE_PROPERTIES"
	if s.Darken {
		env = "env 'FREETYPE_PROPERTIES=" + stemDarkening + "'"
	}
	metrics := "off"
	if s.Metrics {
		metrics = "on"
	}
	cmd := fmt.Sprintf("%s gjs -m %s/specimen.js %s/specimen.json %s", env, typeDir, typeDir, metrics)
	if _, err := g.eval(fmt.Sprintf(`GLib.spawn_command_line_async(%q)`, cmd)); err != nil {
		return run, lfRect{}, err
	}
	var raw string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if out, err := g.run(`cat `+typeDir+`/specimen.json 2>/dev/null`, ""); err == nil && strings.TrimSpace(out) != "" {
			raw = out
			break
		}
	}
	if raw == "" {
		log, _ := g.run(`journalctl -b -q --no-pager -o cat --since -1min | grep -i -E 'gjs|specimen|Gtk' | tail -5`, "")
		return run, lfRect{}, fmt.Errorf("the specimen said nothing in 30 s: %s", oneLine(log))
	}
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		return run, lfRect{}, fmt.Errorf("specimen.json: %w", err)
	}
	var w *struct{ X, Y, W, H int }
	if err := g.evalJSON(`const w = `+specimenWindow+`; w?.activate(global.get_current_time()); return lfWindow(w);`, &w); err != nil || w == nil {
		return run, lfRect{}, fmt.Errorf("no specimen window on the stage%s", errText(err))
	}
	// Drawn once more after it took the focus.
	time.Sleep(time.Second)
	return run, lfRect{w.X, w.Y, w.W, w.H}, nil
}

func (g *guest) stopSpecimen() {
	_, _ = g.run(`pkill -f `+typeDir+`/specimen.js; for i in $(seq 40); do pgrep -f `+typeDir+`/specimen.js >/dev/null || break; sleep 0.25; done`, "")
}

// itemRect is a specimen line in the screenshot: the window's frame on the stage plus the line's
// bounds in it, k physical pixels a stage pixel, outward to whole pixels.
func itemRect(frame lfRect, it specimenItem, k int) image.Rectangle {
	f := float64(k)
	x0 := int(math.Floor((float64(frame.X) + it.X) * f))
	y0 := int(math.Floor((float64(frame.Y) + it.Y) * f))
	x1 := int(math.Ceil((float64(frame.X) + it.X + it.W) * f))
	y1 := int(math.Ceil((float64(frame.Y) + it.Y + it.H) * f))
	return image.Rect(x0, y0, x1, y1)
}

// measureSpecimen measures every line of the specimen in img and saves each row - stems, align and
// word together - as a crop for the page.
func measureSpecimen(img image.Image, run specimenRun, frame lfRect, k int, setting, dir string) ([]typeRow, error) {
	type rowKey struct {
		theme, family string
		weight, size  int
	}
	byRow := map[rowKey]map[string]specimenItem{}
	var order []rowKey
	for _, it := range run.Items {
		if it.Kind == "pangram" {
			continue
		}
		key := rowKey{it.Theme, it.Family, it.Weight, it.Size}
		if byRow[key] == nil {
			byRow[key] = map[string]specimenItem{}
			order = append(order, key)
		}
		byRow[key][it.Kind] = it
	}
	if err := os.MkdirAll(filepath.Join(dir, "crops", setting), 0o755); err != nil {
		return nil, err
	}
	var rows []typeRow
	for _, key := range order {
		items := byRow[key]
		stems, align, word := items["stems"], items["align"], items["word"]
		fg, ok1 := parseHex(stems.FG)
		bg, ok2 := parseHex(stems.BG)
		if !ok1 || !ok2 || stems.W == 0 || align.W == 0 {
			continue
		}
		row := typeRow{Setting: setting, Theme: key.theme, Family: key.family, Weight: key.weight, Size: key.size}
		// 'l' 'l' 'I' 'I' are one stem each; the 'H's two.
		row.Stems = measureStems(coverageOf(img, itemRect(frame, stems, k), fg, bg), map[int]bool{0: true, 1: true, 2: true, 3: true})
		row.Align = measureAlign(coverageOf(img, itemRect(frame, align, k), fg, bg))
		all := itemRect(frame, stems, k).Union(itemRect(frame, align, k)).Union(itemRect(frame, word, k)).Inset(-2 * k)
		name := fmt.Sprintf("%s-%s-%d-%d.png", key.theme, key.family, key.weight, key.size)
		if err := writePNG(filepath.Join(dir, "crops", setting, name), cropOf(img, all)); err != nil {
			return rows, err
		}
		row.Crop = filepath.Join("type", "crops", setting, name)
		rows = append(rows, row)
	}
	return rows, nil
}

// cropOf is r of img as an image of its own.
func cropOf(img image.Image, r image.Rectangle) *image.RGBA {
	r = r.Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := range r.Dy() {
		for x := range r.Dx() {
			out.Set(x, y, img.At(r.Min.X+x, r.Min.Y+y))
		}
	}
	return out
}

// hconcat lays coverages side by side, gap empty columns apart, on the tallest one's height: lines
// of one height measured as one line.
func hconcat(cs []inkMap, gap int) inkMap {
	h, w := 0, 0
	for _, c := range cs {
		h = max(h, c.h)
		w += c.w + gap
	}
	out := inkMap{w: w, h: h, a: make([]float64, w*h)}
	x0 := 0
	for _, c := range cs {
		for y := range c.h {
			copy(out.a[y*w+x0:y*w+x0+c.w], c.a[y*c.w:(y+1)*c.w])
		}
		x0 += c.w + gap
	}
	return out
}

// pixelsDiffer counts the pixels of r, a stage rectangle at scale k, that differ at all between a
// and b.
func pixelsDiffer(a, b image.Image, r image.Rectangle, k int) int {
	p := image.Rect(r.Min.X*k, r.Min.Y*k, r.Max.X*k, r.Max.Y*k).Intersect(a.Bounds()).Intersect(b.Bounds())
	n := 0
	for y := p.Min.Y; y < p.Max.Y; y++ {
		for x := p.Min.X; x < p.Max.X; x++ {
			if at(a, x, y) != at(b, x, y) {
				n++
			}
		}
	}
	return n
}

// shellType opens the date menu - the top bar's clock, the menu's labels, the calendar - shoots it
// and measures each, light text on the shell's dark surfaces.
func (r *report) shellType(v *vm, g *guest, setting string, k int, fg color.NRGBA, dir string) ([]typeShell, error) {
	_, _ = g.eval(`Main.panel.statusArea.dateMenu.menu.open(); true`)
	defer func() { _, _ = g.eval(`Main.panel.statusArea.dateMenu.menu.close(); true`) }()
	time.Sleep(2 * time.Second)
	var s struct {
		Clock  *lfLook
		Labels []*lfLook
		Days   []*lfLook
	}
	if err := g.evalJSON(lfDays+`
		const cal = new Set(days);
		const inCal = a => { for (let p = a; p; p = p.get_parent()) if (cal.has(p)) return true; return false; };
		const first = days.find(b => !b.has_style_class_name('calendar-other-month'));
		const week = first ? days.filter(b => Math.abs(lfBox(b).y - lfBox(first).y) < 2) : [];
		return {clock: look(dm._clockDisplay), labels: lfLabels(dm.menu.box).filter(t => t.text.length > 2), days: week.map(look)};`, &s); err != nil {
		return nil, err
	}
	name := "type/shell-" + setting
	r.shot(v, name)
	img, err := readPNG(filepath.Join(r.dir, name+".png"))
	if err != nil {
		return nil, err
	}
	px := func(a *lfLook) image.Rectangle {
		return image.Rect(a.X*k, a.Y*k, (a.X+a.W)*k, (a.Y+a.H)*k)
	}
	measure := func(item string, looks []*lfLook) typeShell {
		var cs []inkMap
		var box image.Rectangle
		for _, a := range looks {
			if a == nil || a.W == 0 {
				continue
			}
			bg, _ := modeColor(img, lfRect{a.X * k, a.Y * k, a.W * k, a.H * k})
			cs = append(cs, coverageOf(img, px(a), fg, bg))
			box = box.Union(px(a))
		}
		t := typeShell{Setting: setting, Item: item, Stems: measureStems(hconcat(cs, 4), nil)}
		file := filepath.Join(dir, "crops", setting, "shell-"+item+".png")
		if os.MkdirAll(filepath.Dir(file), 0o755) == nil && !box.Empty() && writePNG(file, cropOf(img, box.Inset(-2*k))) == nil {
			t.Crop = filepath.Join("type", "crops", setting, "shell-"+item+".png")
		}
		return t
	}
	return []typeShell{
		measure("top bar clock", []*lfLook{s.Clock}),
		measure("menu labels", s.Labels),
		measure("calendar week", s.Days),
	}, nil
}

// typeSummary is a setting's rows averaged, per theme: what a glance at the matrix compares.
type typeSummary struct {
	Edge, Peak, StemSD, BaseJitter, XJitter, HEdge float64
	N                                              int
}

func summarize(rows []typeRow, setting, theme string, sizes func(int) bool) typeSummary {
	var s typeSummary
	for _, r := range rows {
		if r.Setting != setting || r.Theme != theme || !sizes(r.Size) {
			continue
		}
		s.Edge += r.Stems.Edge
		s.Peak += r.Stems.Peak
		s.StemSD += math.Sqrt(r.Stems.StemVar)
		s.BaseJitter += r.Align.BaseJitter
		s.XJitter += r.Align.XJitter
		s.HEdge += r.Align.HEdge
		s.N++
	}
	if s.N > 0 {
		n := float64(s.N)
		s.Edge, s.Peak, s.StemSD, s.BaseJitter, s.XJitter, s.HEdge = s.Edge/n, s.Peak/n, s.StemSD/n, s.BaseJitter/n, s.XJitter/n, s.HEdge/n
	}
	return s
}

// typePage is the contact sheet: the settings averaged, what GTK draws identically, the shell's
// text, and every line under every setting side by side at one image pixel to one screen pixel.
func typePage(rep typeReport) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Type matrix</title><style>
:root{--bg:#0B1116;--fg:#E6EDF2;--muted:#9FB3C0;--line:#2A3843;--bad:#FF7A90;--z:1;--dpr:1}
body{background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif;margin:16px}
h1{font-size:20px}h2{font-size:16px;margin:28px 0 8px}h3{font-size:14px;margin:18px 0 6px;color:var(--muted)}
table{border-collapse:collapse;font-variant-numeric:tabular-nums;margin:8px 0}td,th{border:1px solid var(--line);padding:3px 8px;text-align:right}
th:first-child,td:first-child{text-align:left}.row{display:flex;flex-wrap:wrap;gap:12px}
figure{margin:0}figcaption{font-size:12px;color:var(--muted);max-width:420px}
img.px{image-rendering:pixelated;display:block}small{color:var(--muted)}.bad{color:var(--bad)}
.wrap{overflow-x:auto}label{margin-right:12px}
</style><h1>Type matrix</h1>`)
	fmt.Fprintf(&b, `<p><small>Screen %s at %d×; %s</small></p>`, html.EscapeString(rep.Screen), rep.Scale, html.EscapeString(rep.Packages))
	b.WriteString(`<p>Pixels: <label><input type="radio" name="z" value="1" checked> 1:1 on this screen</label><label><input type="radio" name="z" value="4"> 4× (pixelated)</label></p>
<p><small>Edge: 10-90% width of a stem's edge, px (0.8 is a hard step). Peak: how much of the text's colour a one-stem glyph reaches. Stem σ: how much stems of one glyph line differ in width. Base σ, x σ: how far glyphs' baselines and x-heights stray, px. H-edge: 10-90% width of tops and bottoms (blur across a horizontal).</small></p>`)
	all := func(int) bool { return true }
	ui := func(s int) bool { return s <= 11 }
	b.WriteString(`<h2>Settings, averaged</h2><div class="wrap"><table><tr><th>setting</th><th>theme</th><th>sizes</th><th>edge</th><th>peak</th><th>stem σ</th><th>base σ</th><th>x σ</th><th>h-edge</th><th>why</th></tr>`)
	for _, s := range rep.Settings {
		for _, theme := range []string{"dark", "light"} {
			for _, sz := range []struct {
				name string
				f    func(int) bool
			}{{"9-11", ui}, {"all", all}} {
				m := summarize(rep.Rows, s.Name, theme, sz.f)
				if m.N == 0 {
					continue
				}
				fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%.2f</td><td>%.2f</td><td>%.3f</td><td>%.3f</td><td>%.3f</td><td>%.2f</td><td>%s</td></tr>`,
					html.EscapeString(s.Name), theme, sz.name, m.Edge, m.Peak, m.StemSD, m.BaseJitter, m.XJitter, m.HEdge, html.EscapeString(s.Why))
			}
		}
	}
	b.WriteString(`</table></div>`)
	b.WriteString(`<h2>What GTK draws the same</h2><table><tr><th>setting</th><th>against</th><th>pixels that differ</th></tr>`)
	for _, s := range rep.Same {
		cls := ""
		if s.Differ != 0 {
			cls = ` class="bad"`
		}
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td%s>%d</td></tr>`, html.EscapeString(s.Setting), html.EscapeString(s.Same), cls, s.Differ)
	}
	b.WriteString(`</table><h2>The shell's text</h2><div class="wrap"><table><tr><th>setting</th><th>item</th><th>edge</th><th>peak</th><th>stem σ</th><th>stems</th></tr>`)
	for _, s := range rep.Shell {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%.2f</td><td>%.2f</td><td>%.3f</td><td>%d</td></tr>`,
			html.EscapeString(s.Setting), html.EscapeString(s.Item), s.Stems.Edge, s.Stems.Peak, math.Sqrt(s.Stems.StemVar), s.Stems.Stems)
	}
	b.WriteString(`</table></div>`)
	byItem := map[string][]typeShell{}
	var items []string
	for _, s := range rep.Shell {
		if byItem[s.Item] == nil {
			items = append(items, s.Item)
		}
		byItem[s.Item] = append(byItem[s.Item], s)
	}
	for _, it := range items {
		fmt.Fprintf(&b, `<h3>shell: %s</h3><div class="row">`, html.EscapeString(it))
		for _, s := range byItem[it] {
			if s.Crop != "" {
				fmt.Fprintf(&b, `<figure><img class="px" src="%s"><figcaption>%s · edge %.2f · peak %.2f</figcaption></figure>`,
					html.EscapeString(s.Crop), html.EscapeString(s.Setting), s.Stems.Edge, s.Stems.Peak)
			}
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<h2>Every line</h2>`)
	type group struct {
		theme, family string
		weight, size  int
	}
	byGroup := map[group][]typeRow{}
	var groups []group
	for _, r := range rep.Rows {
		g := group{r.Theme, r.Family, r.Weight, r.Size}
		if byGroup[g] == nil {
			groups = append(groups, g)
		}
		byGroup[g] = append(byGroup[g], r)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, c := groups[i], groups[j]
		if a.theme != c.theme {
			return a.theme < c.theme
		}
		if a.family != c.family {
			return a.family > c.family
		}
		if a.size != c.size {
			return a.size < c.size
		}
		return a.weight < c.weight
	})
	for _, g := range groups {
		fmt.Fprintf(&b, `<h3>%s · Adwaita %s · %d · %d pt</h3><div class="row">`, g.theme, map[string]string{"sans": "Sans", "mono": "Mono"}[g.family], g.weight, g.size)
		for _, r := range byGroup[g] {
			fmt.Fprintf(&b, `<figure><img class="px" src="%s"><figcaption>%s · edge %.2f · peak %.2f · stem %.2f σ %.3f · base σ %.3f · x σ %.3f · h-edge %.2f</figcaption></figure>`,
				html.EscapeString(r.Crop), html.EscapeString(r.Setting), r.Stems.Edge, r.Stems.Peak, r.Stems.StemMean, math.Sqrt(r.Stems.StemVar),
				r.Align.BaseJitter, r.Align.XJitter, r.Align.HEdge)
		}
		b.WriteString(`</div>`)
	}
	// One image pixel to one device pixel: CSS pixels are devicePixelRatio of them.
	b.WriteString(`<script>
const size = () => { const z = +document.querySelector('input[name=z]:checked').value;
  for (const i of document.querySelectorAll('img.px')) if (i.naturalWidth) i.style.width = (i.naturalWidth * z / devicePixelRatio) + 'px'; };
document.querySelectorAll('input[name=z]').forEach(r => r.addEventListener('change', size));
document.querySelectorAll('img.px').forEach(i => i.addEventListener('load', size));
matchMedia('(resolution: ' + devicePixelRatio + 'dppx)').addEventListener('change', size);
size();
</script></html>`)
	return b.String()
}
