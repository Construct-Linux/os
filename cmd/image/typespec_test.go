package main

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What GTK's Wayland backend makes of the keys, and that a setting GTK did not hear fails.
func TestExpectedGtk(t *testing.T) {
	e := expectedGtk("automatic", "slight", "grayscale", "rgb", false)
	if e != (gtkSeen{FontRendering: "automatic", XftHinting: 1, XftHintstyle: "hintslight", XftAntialias: 1, XftRGBA: "none"}) {
		t.Errorf("default: %+v", e)
	}
	if e := expectedGtk("manual", "none", "rgba", "bgr", true); e.XftHinting != 0 || e.XftHintstyle != "hintnone" || e.XftRGBA != "bgr" || !e.HintFontMetrics {
		t.Errorf("manual none rgba: %+v", e)
	}
	if e := expectedGtk("manual", "full", "none", "rgb", false); e.XftAntialias != 0 {
		t.Errorf("no antialiasing: %+v", e)
	}
	seen := e
	seen.XftDPI = 98304
	if !seen.matches(e) {
		t.Error("the DPI is not a rendering setting")
	}
	seen.XftHintstyle = "hintfull"
	if seen.matches(e) {
		t.Error("a hint style GTK did not hear matched")
	}
}

// Every setting has a name, a reason, and a same_as that is another setting's name.
func TestTypeSettings(t *testing.T) {
	names := map[string]bool{}
	for _, s := range typeSettings {
		if s.Name == "" || s.Why == "" || names[s.Name] {
			t.Errorf("setting %+v", s)
		}
		names[s.Name] = true
	}
	for _, s := range typeSettings {
		if s.Same != "" && !names[s.Same] {
			t.Errorf("%s is the same as %q, which is no setting", s.Name, s.Same)
		}
	}
	if typeSettings[0].Name != "default" || len(typeSettings[0].values()[0]) != 0 {
		t.Error("the first setting must be the image's own: the type-specimen view")
	}
}

// A line's bounds in the window, on the stage at the window's frame, k physical pixels each, out
// to whole pixels.
func TestItemRect(t *testing.T) {
	it := specimenItem{X: 16, Y: 10.5, W: 100.25, H: 14}
	if r := itemRect(lfRect{0, 0, 1440, 900}, it, 2); r != image.Rect(32, 21, 233, 49) {
		t.Errorf("%v", r)
	}
	if r := itemRect(lfRect{5, 7, 100, 100}, it, 1); r != image.Rect(21, 17, 122, 32) {
		t.Errorf("framed %v", r)
	}
}

func TestHconcatAndDiffer(t *testing.T) {
	a := inkMap{w: 2, h: 1, a: []float64{1, 0.5}}
	b := inkMap{w: 1, h: 2, a: []float64{0.25, 0.75}}
	c := hconcat([]inkMap{a, b}, 1)
	if c.w != 5 || c.h != 2 || c.at(0, 0) != 1 || c.at(1, 0) != 0.5 || c.at(3, 0) != 0.25 || c.at(3, 1) != 0.75 || c.at(0, 1) != 0 {
		t.Errorf("%+v", c)
	}
	x, y := solid(20, 20, tInk), solid(20, 20, tInk)
	y.SetNRGBA(3, 3, tAccent)
	y.SetNRGBA(15, 15, tAccent)
	if n := pixelsDiffer(x, y, image.Rect(0, 0, 5, 5), 2); n != 1 {
		t.Errorf("differ %d", n)
	}
}

// The page: its tables, a crop for each line, and one image pixel to one device pixel.
func TestTypePage(t *testing.T) {
	row := typeRow{Setting: "default", Theme: "dark", Family: "sans", Weight: 400, Size: 9,
		Stems: stemStats{Edge: 0.8, Peak: 1}, Crop: "type/crops/default/dark-sans-400-9.png"}
	rep := typeReport{Screen: "2880x1800", Scale: 2, Settings: typeSettings[:1], Rows: []typeRow{row},
		Shell: []typeShell{{Setting: "default", Item: "top bar clock", Crop: "type/crops/default/shell-top bar clock.png"}},
		Same:  []typeSame{{Setting: "auto-none", Same: "default", Differ: 3}}}
	p := typePage(rep)
	for _, want := range []string{"<title>Type matrix</title>", "dark-sans-400-9.png", "devicePixelRatio", `class="bad">3<`, "top bar clock", "Adwaita Sans · 400 · 9 pt"} {
		if !strings.Contains(p, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if s := summarize(rep.Rows, "default", "dark", func(int) bool { return true }); s.N != 1 || s.Edge != 0.8 {
		t.Errorf("summary %+v", s)
	}
	if s := summarize(rep.Rows, "default", "light", func(int) bool { return true }); s.N != 0 {
		t.Errorf("light summary %+v", s)
	}
}

// measureSpecimen measures and crops each row it was told of, and leaves the pangram alone.
func TestMeasureSpecimen(t *testing.T) {
	img := solid(400, 100, tTypeBG)
	ink(img, 20, 20, 23, 40)   // l
	ink(img, 30, 20, 33, 40)   // l
	ink(img, 40, 20, 43, 40)   // I
	ink(img, 50, 20, 53, 40)   // I
	ink(img, 250, 26, 256, 40) // x
	ink(img, 265, 26, 271, 40) // z
	fg, bg := hexOf(tTypeFG), hexOf(tTypeBG)
	item := func(kind string, x float64) specimenItem {
		return specimenItem{Theme: "dark", FG: fg, BG: bg, Family: "sans", Weight: 400, Size: 9, Kind: kind, X: x, Y: 8, W: 90, H: 20}
	}
	run := specimenRun{Items: []specimenItem{item("stems", 5), item("align", 120), item("word", 150), item("pangram", 0)}}
	dir := t.TempDir()
	rows, err := measureSpecimen(img, run, lfRect{}, 2, "default", dir)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	r := rows[0]
	if !near64(r.Stems.Peak, 1, 0.01) || r.Stems.Stems != 4 || r.Align.Glyphs != 2 || r.Align.BaseJitter > 1e-6 {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "crops", "default", "dark-sans-400-9.png")); err != nil {
		t.Error(err)
	}
}
