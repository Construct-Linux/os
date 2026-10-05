package main

import (
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Construct-Linux/os/internal/css"
	"github.com/Construct-Linux/os/internal/needle"
	"github.com/Construct-Linux/os/internal/pixels"
)

var (
	tInk    = color.NRGBA{0x0B, 0x11, 0x16, 255}
	tG      = color.NRGBA{0x17, 0x20, 0x28, 255}
	tH      = color.NRGBA{0x1F, 0x2A, 0x33, 255}
	tRing   = color.NRGBA{0x2A, 0x38, 0x43, 255}
	tAccent = color.NRGBA{0x00, 0xE5, 0xFF, 255}
	tBlack  = color.NRGBA{0, 0, 0, 255}
)

func fill(img *image.NRGBA, r pixels.Rect, c color.NRGBA) {
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// coverage is how much of pixel (x, y) a shape covers, by 4x4 supersampling: the antialiasing a
// renderer gives an edge.
func coverage(x, y int, inside func(fx, fy float64) bool) float64 {
	n := 0
	for i := range 4 {
		for j := range 4 {
			if inside(float64(x)+(float64(i)+0.5)/4, float64(y)+(float64(j)+0.5)/4) {
				n++
			}
		}
	}
	return float64(n) / 16
}

func blendAt(img *image.NRGBA, x, y int, c color.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	img.SetNRGBA(x, y, pixels.Tint(c, pixels.At(img, x, y), a))
}

const tPalette = `{"brand-boot":"#000000","neutral-e":"#0B1116","neutral-f":"#11181E","neutral-g":"#172028",
"neutral-h":"#1F2A33","dark-border":"#2A3843","dark-accent":"#00E5FF","dark-accent-text":"#00E5FF",
"dark-text":"#E6EDF2","dark-muted":"#9FB3C0","dark-on-accent":"#0B1116","light-accent":"#007A85","light-accent-text":"#00646D",
"light-on-accent":"#FFFFFF","light-text":"#11181E","light-muted":"#536672","light-desk":"#DEE4E8","light-shelf":"#E9EDF0",
"neutral-a":"#FFFFFF","line-light-ring":0.4,"line-light-border":0.3,"ansi-dark-blue":"#5DB2F7",
"tint-dark-rest":0.16,"tint-dark-hover":0.23,"tint-dark-active":0.28,"shape-inset":6,"shape-radius-control":9,"shape-radius-surface":15,"shape-radius-sheet":24,
"shape-space":[3,6,12,18,24],"type-ui":"Adwaita Sans","type-mono":"Adwaita Mono"}`

// palette.json is flat and mixed - colours, numbers, families, a list - and a token a check needs
// that it lacks is named, never read as black.
func TestParseLFPalette(t *testing.T) {
	p, err := parseLFPalette([]byte(tPalette))
	if err != nil {
		t.Fatal(err)
	}
	if p.colors["neutral-g"] != tG || p.nums["tint-dark-rest"] != 0.16 || p.strs["type-ui"] != "Adwaita Sans" {
		t.Errorf("read %v %v %v", p.colors["neutral-g"], p.nums["tint-dark-rest"], p.strs["type-ui"])
	}
	if !p.has(color.NRGBA{0x2A, 0x38, 0x43, 0x40}) || p.has(color.NRGBA{0x28, 0x28, 0x28, 255}) {
		t.Error("has: a palette colour at an alpha is the palette's, a grey is not")
	}
	_, err = parseLFPalette([]byte(strings.Replace(tPalette, `"neutral-h":"#1F2A33",`, "", 1)))
	if err == nil || !strings.Contains(err.Error(), "neutral-h") {
		t.Errorf("a missing token: %v", err)
	}
}

// The film: systemd-boot's menu by its grey bar, Plymouth by the mark's cyan on black (not the
// firmware's blue logo, not the wordmark's white), and the share a wrong ground covers.
func TestFilmStats(t *testing.T) {
	rgba := func(img *image.NRGBA) *image.RGBA {
		out := image.NewRGBA(img.Bounds())
		for i := range len(img.Pix) {
			out.Pix[i] = img.Pix[i]
		}
		return out
	}
	menu := solid(400, 200, tBlack)
	fill(menu, pixels.Rect{X: 100, Y: 90, W: 200, H: 16}, color.NRGBA{0x98, 0x98, 0x98, 255})
	if f := filmStats(rgba(menu)); !f.menu || f.plymouth() {
		t.Errorf("menu: %+v", f)
	}
	logo := solid(400, 200, tBlack)
	fill(logo, pixels.Rect{X: 100, Y: 90, W: 40, H: 40}, color.NRGBA{0x30, 0x60, 0xC0, 255})
	fill(logo, pixels.Rect{X: 150, Y: 90, W: 60, H: 10}, color.NRGBA{0xF0, 0xF8, 0xFF, 255})
	if f := filmStats(rgba(logo)); f.cyan != 0 || f.menu {
		t.Errorf("the firmware's logo: %+v", f)
	}
	ply := solid(400, 200, tBlack)
	fill(ply, pixels.Rect{X: 150, Y: 40, W: 100, H: 2}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	fill(ply, pixels.Rect{X: 150, Y: 140, W: 100, H: 2}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	fill(ply, pixels.Rect{X: 150, Y: 40, W: 2, H: 100}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	f := filmStats(rgba(ply))
	if !f.plymouth() || f.mark != (pixels.Rect{X: 150, Y: 40, W: 100, H: 102}) {
		t.Errorf("Plymouth: %+v", f)
	}
	ground := solid(400, 200, tBlack)
	fill(ground, pixels.Rect{X: 0, Y: 0, W: 400, H: 120}, color.NRGBA{0x28, 0x28, 0x28, 255})
	if f := filmStats(rgba(ground)); math.Abs(f.cover-0.6) > 1e-9 || f.coverHex != "#282828" {
		t.Errorf("SystemBackground: %+v", f)
	}
}

// The film's report: a wrong ground after Plymouth fails it, one before does not; the menu's time
// on screen is reported; the mark is where Plymouth drew it whole.
func TestFilmReport(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	whole := pixels.Rect{X: 100, Y: 100, W: 200, H: 200}
	frames := []filmFrame{
		{at: ms(0), black: 1},
		{at: ms(100), black: 0.99, menu: true},
		{at: ms(1500), black: 0.99, menu: true},
		{at: ms(1600), cover: 0.9, coverHex: "#282828"},
		{at: ms(2000), black: 0.95, cyan: 500, w: 400, h: 400, mark: pixels.Rect{X: 100, Y: 200, W: 200, H: 100}},
		{at: ms(2100), black: 0.95, cyan: 900, w: 400, h: 400, mark: whole},
		{at: ms(2200), black: 0.95, cyan: 900, w: 400, h: 400, mark: whole},
		{at: ms(3000), cover: 0.3, coverHex: "#11181E"},
	}
	r := &report{dir: t.TempDir()}
	mark := r.filmReport(frames, 0)
	if mark == nil || mark[0] != 0.5 || mark[1] != 0.5 {
		t.Errorf("mark %v", mark)
	}
	got := map[string]result{}
	for _, x := range r.results {
		got[x.name] = x
	}
	if g := got["boot film: ground"]; !g.ok || !strings.Contains(g.detail, "#11181E 30%") {
		t.Errorf("ground: %+v", g)
	}
	if g := got["boot film: ground"]; !strings.Contains(g.detail, "menu on screen 1.5s") {
		t.Errorf("menu: %+v", g)
	}
	frames = append(frames, filmFrame{at: ms(3100), cover: 0.5, coverHex: "#282828"})
	r = &report{dir: t.TempDir()}
	r.filmReport(frames, 0)
	if r.results[0].ok {
		t.Errorf("half the screen #282828 after Plymouth passed: %+v", r.results[0])
	}
	r = &report{dir: t.TempDir()}
	if r.filmReport(frames[:4], 2) != nil || r.results[0].ok {
		t.Errorf("no Plymouth: %+v", r.results)
	}
}

// Plymouth's prompt: the accent's ring around g, its corner the boot's black; a flat box without a
// ring is not it. After the password the grid's lines are back.
func TestPromptLook(t *testing.T) {
	prompt := solid(1280, 800, tBlack)
	pill := pixels.Rect{X: 480, Y: 594, W: 320, H: 36}
	for y := pill.Y; y < pill.Y+pill.H; y++ {
		for x := pill.X; x < pill.X+pill.W; x++ {
			in := func(r float64) func(fx, fy float64) bool {
				return func(fx, fy float64) bool {
					cx := math.Min(math.Max(fx, 480+18), 800-18)
					return math.Hypot(fx-cx, fy-612) <= r
				}
			}
			blendAt(prompt, x, y, tAccent, coverage(x, y, in(18)))
			blendAt(prompt, x, y, tG, coverage(x, y, in(16)))
		}
	}
	if ok, detail := promptLook(prompt, tAccent, tG, tBlack); !ok {
		t.Error(detail)
	}
	flat := solid(1280, 800, tBlack)
	fill(flat, pill, color.NRGBA{0x1B, 0x22, 0x2A, 255})
	if ok, _ := promptLook(flat, tAccent, tG, tBlack); ok {
		t.Error("a box without a ring passed")
	}
	after := solid(1280, 800, tBlack)
	fill(prompt, pixels.Rect{X: 500, Y: 140, W: 280, H: 3}, tAccent)
	fill(after, pixels.Rect{X: 500, Y: 140, W: 280, H: 3}, tAccent)
	fill(after, pixels.Rect{X: 500, Y: 240, W: 280, H: 3}, tAccent)
	if ok, detail := gridBack(prompt, after, 594); !ok {
		t.Error(detail)
	}
	if ok, _ := gridBack(prompt, prompt, 594); ok {
		t.Error("the outro's last frame again passed as the grid back")
	}
}

// The grid after the password is its own result: a screen that cannot be read, or none at all, is
// no grid, and one readable screen with the grid back is.
func TestGridPlymouth(t *testing.T) {
	dir := t.TempDir()
	prompt := solid(1280, 800, tBlack)
	fill(prompt, pixels.Rect{X: 500, Y: 140, W: 280, H: 3}, tAccent)
	back := solid(1280, 800, tBlack)
	fill(back, pixels.Rect{X: 500, Y: 140, W: 280, H: 3}, tAccent)
	fill(back, pixels.Rect{X: 500, Y: 240, W: 280, H: 3}, tAccent)
	same, grid, broken := filepath.Join(dir, "same.png"), filepath.Join(dir, "grid.png"), filepath.Join(dir, "broken.png")
	if pixels.WritePNG(same, prompt) != nil || pixels.WritePNG(grid, back) != nil || os.WriteFile(broken, []byte("\x89PNG partial"), 0o644) != nil {
		t.Fatal("writing the screens")
	}
	for _, c := range []struct {
		name   string
		afters []string
		want   bool
	}{
		{"none", nil, false},
		{"unreadable", []string{broken}, false},
		{"unreadable, then the outro still", []string{broken, same}, false},
		{"unreadable, then the grid", []string{broken, grid}, true},
	} {
		if ok, why := gridPlymouth(prompt, c.afters); ok != c.want {
			t.Errorf("%s: %v (%s), want %v", c.name, ok, why, c.want)
		}
	}
}

const tCSS = `
/* the bar */
#panel { background-color: #172028; border: 1px solid #2a3843; font-weight: normal; }
#panel.unlock-screen, #panel.login-screen { background-color: transparent; border-color: transparent; }
#uiGroup .dashtopanelPanel.dock { border: 1px solid #2A3843; }
.popup-menu .popup-menu-content { background-color: #1f2a33; border: 1px solid #2A3843; }
.popup-menu .popup-menu-content:hover { background-color: #3584e4; }
.dash-label { background: rgba(0, 0, 0, 0.65); border: none; font-weight: 800; }
.button:focus { box-shadow: inset 0 0 0 2px #00e5ff; }
.quick-toggle:focus { box-shadow: none; }
.popup-menu-item:focus, .calendar-day:focus { box-shadow: none; }
.pager-button:hover, .pager-button:focus { box-shadow: none; }
.pager-button:focus { box-shadow: inset 0 0 0 2px #00e5ff; }
.clock-display:focus { box-shadow: none; }
.clock-display:focus .clock { box-shadow: inset 0 0 0 2px #00e5ff; }
.osd-window { background-color: #282828; color: rgba(255, 255, 255, 0.5); background-image: url("assets/a;b.svg"); }
`

// The CSS checks read St's CSS as the shell loaded it: rules, the subject a rule styles at rest,
// and each check names what broke it.
func TestCSSChecks(t *testing.T) {
	p, err := parseLFPalette([]byte(tPalette))
	if err != nil {
		t.Fatal(err)
	}
	rules := css.Parse(tCSS)
	if len(rules) != 14 {
		t.Fatalf("%d rules", len(rules))
	}
	if m, _ := css.Subject(rules, ".osd-window"); m["background-image"] != `url("assets/a;b.svg")` {
		t.Errorf("a ; inside url() split the declaration: %q", m["background-image"])
	}
	if m, _ := css.Subject(rules, ".popup-menu-content"); m["background-color"] != "#1f2a33" {
		t.Errorf("a :hover rule styled the menu at rest: %v", m)
	}

	ok, detail := cssContract(rules, tCSS, p)
	for _, want := range []string{".dash-label: background", ".dash-label: border", ".osd-window: background", ".window-caption: no rule", "0.65"} {
		if !strings.Contains(detail, want) {
			t.Errorf("G1 does not name %q: %s", want, detail)
		}
	}
	if ok || strings.Contains(detail, "#panel:") || strings.Contains(detail, ".popup-menu-content:") || strings.Contains(detail, ".dashtopanelPanel.dock:") {
		t.Errorf("G1: %v %s", ok, detail)
	}

	ok, detail = cssFocus(rules, tAccent)
	if ok || !strings.Contains(detail, "3 rules") || !strings.Contains(detail, ".quick-toggle:focus") || !strings.Contains(detail, ".calendar-day:focus") ||
		strings.Contains(detail, "popup-menu-item") ||
		strings.Contains(detail, "pager-button") || strings.Contains(detail, "clock-display") {
		t.Errorf("G2: %v %s", ok, detail)
	}

	ok, detail = cssPalette(rules, p)
	if ok || !strings.Contains(detail, "#3584E4") || !strings.Contains(detail, "#282828 (grey)") || strings.Contains(detail, "#FFFFFF") {
		t.Errorf("G3: %v %s", ok, detail)
	}
	if ok, detail := cssPalette(css.Parse(`#panel { color: rgba(230, 237, 242, 0.08); border-color: transparent; }`), p); !ok {
		t.Errorf("G3, a palette colour at an alpha: %s", detail)
	}

	ok, detail = cssWeights(rules)
	if ok || !strings.Contains(detail, ".dash-label: 800") || strings.Contains(detail, "#panel") {
		t.Errorf("G6: %v %s", ok, detail)
	}
}

// C1 as G3 reads it: the accent and white at an alpha over a surface, written opaque, are the
// palette's; a mix of the accent and the error colour, or a grey, are not.
func TestComposite(t *testing.T) {
	p, err := parseLFPalette([]byte(tPalette))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []color.NRGBA{pixels.Tint(tAccent, tH, 0.5), pixels.Tint(color.NRGBA{255, 255, 255, 255}, tH, 0.06), pixels.Tint(tInk, tAccent, 0.08)} {
		if !p.composite(c) {
			t.Errorf("%s is a composite", pixels.Hex(c))
		}
	}
	for _, c := range []color.NRGBA{{0x28, 0x28, 0x28, 255}, {0xE3, 0x59, 0x84, 255}, {0x2D, 0x46, 0x5A, 255}} {
		if p.composite(c) {
			t.Errorf("%s taken for a composite", pixels.Hex(c))
		}
	}
}

// C8: a ring of the accent is focus's; decoration's 1 px edge on a tint and the Alt+Esc frame are
// not rings to hold.
func TestAccentRings(t *testing.T) {
	sheet := `
.button:focus { box-shadow: inset 0 0 0 2px #00E5FF; }
.calendar-day:selected:focus { box-shadow: inset 0 0 0 2px #00E5FF !important; }
.calendar-today { box-shadow: inset 0 0 0 2px #00E5FF; }
.workspace-thumbnail-indicator { border: 2px solid #00E5FF; border-radius: 3px; }
.tile-preview { background-color: rgba(0, 229, 255, 0.08); border: 1px solid #00E5FF; border-radius: 9px; }
.cycler-highlight { border: 5px solid #00E5FF; }
.slider { box-shadow: 0 2px 4px #00E5FF; }
.quick-toggle:checked .quick-toggle-icon { background-color: #00E5FF; }
.magnifier-zoom-region.full-screen { border-width: 0; }`
	ok, detail := cssAccentRings(css.Parse(sheet), tAccent)
	if ok || !strings.Contains(detail, ".calendar-today") || !strings.Contains(detail, ".workspace-thumbnail-indicator") ||
		strings.Contains(detail, ".button") || strings.Contains(detail, ":selected") || strings.Contains(detail, "tile-preview") || strings.Contains(detail, "cycler") ||
		strings.Contains(detail, ".slider") || strings.Contains(detail, "quick-toggle") || strings.Contains(detail, "magnifier") {
		t.Errorf("C8: %v %s", ok, detail)
	}
}

// GTK's CSS keeps most of a scheme's rules in @media blocks: they are rules of the sheet.
func TestParseCSSMedia(t *testing.T) {
	rules := css.Parse(`@import url("a.css");
window { color: red; }
@media (prefers-color-scheme: dark) {
label.title { font-weight: 800; }
}
@keyframes spin { to { transform: rotate(1turn); } }`)
	if len(rules) != 2 {
		t.Fatalf("%d rules: %v", len(rules), rules)
	}
	if ok, detail := cssWeights(rules); ok || !strings.Contains(detail, "label.title: 800") {
		t.Errorf("G6 in a @media block: %s", detail)
	}
}

// Plymouth's prompt at 2880x1800, drawn at Plymouth's scale 2 where the 1440x900 layout puts it.
func TestPromptLookScaled(t *testing.T) {
	if from, to := promptRows(800); from != 560 || to != 680 {
		t.Errorf("1280x800 rows %d-%d", from, to)
	}
	prompt := solid(2880, 1800, tBlack)
	// The pill's middle at 678 of 900 logical, 1356 physical; 320x36 logical.
	cy, half := 1356.0, 320.0
	for y := 1320; y < 1392; y++ {
		for x := 1440 - 320; x < 1440+320; x++ {
			in := func(r float64) func(fx, fy float64) bool {
				return func(fx, fy float64) bool {
					cx := math.Min(math.Max(fx, 1440-half+36), 1440+half-36)
					return math.Hypot(fx-cx, fy-cy) <= r
				}
			}
			blendAt(prompt, x, y, tAccent, coverage(x, y, in(36)))
			blendAt(prompt, x, y, tG, coverage(x, y, in(32)))
		}
	}
	if ok, detail := promptLook(prompt, tAccent, tG, tBlack); !ok {
		t.Error(detail)
	}
}

// Plymouth draws on the firmware's framebuffer first: the mark is read only from the frames of
// the last screen it drew on, as shares of it.
func TestFilmReportSizes(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	frames := []filmFrame{
		{at: ms(1000), black: 0.95, cyan: 900, w: 1280, h: 800, mark: pixels.Rect{X: 540, Y: 240, W: 200, H: 200}},
		{at: ms(1100), black: 0.95, cyan: 900, w: 1280, h: 800, mark: pixels.Rect{X: 540, Y: 240, W: 200, H: 200}},
		{at: ms(2000), black: 0.95, cyan: 3600, w: 2880, h: 1800, mark: pixels.Rect{X: 1240, Y: 600, W: 400, H: 400}},
	}
	r := &report{dir: t.TempDir()}
	if mark := r.filmReport(frames, 0); mark == nil || mark[0] != 0.5 || mark[1] != 800.0/1800 {
		t.Errorf("mark %v", mark)
	}
}

// testLF is an lf over a report of its own, the test palette, at scale k.
func testLF(t *testing.T, k int) *lf {
	t.Helper()
	p, err := parseLFPalette([]byte(tPalette))
	if err != nil {
		t.Fatal(err)
	}
	return &lf{r: &report{dir: t.TempDir()}, pal: p, ok: true, k: k, stageW: 640, stageH: 400}
}

// lastResult is the last check a report recorded.
func lastResult(t *testing.T, r *report) result {
	t.Helper()
	if len(r.results) == 0 {
		t.Fatal("no check recorded")
	}
	return r.results[len(r.results)-1]
}

// A check counts on the view being measured, a failure by its name; a check on the guest counts on
// none.
func TestCheckCountsOnView(t *testing.T) {
	l := testLF(t, 1)
	l.r.views = map[string]*needle.Spec{"shell-calendar": {}}
	l.cur = "shell-calendar"
	l.check("today", "calendar", true, "")
	l.check("focus", "calendar day", false, "")
	l.cur = ""
	l.check("fonts", "G4", false, "")
	got := l.r.views["shell-calendar"].LF
	if got.Pass != 1 || got.Fail != 1 || !slices.Equal(got.Checks, []string{"focus calendar day"}) {
		t.Errorf("counted %+v", got)
	}
	l.r.viewSweep("shell-calendar", []pixels.Rect{{X: 1, Y: 2, W: 30, H: 40}}, 0.004)
	if v := l.r.lookViewOf("shell-calendar"); len(v.Sweep.Areas) != 1 || v.Sweep.Areas[0].W != 30 || v.LF.Fail != 1 {
		t.Errorf("look.json's view %+v", v)
	}
}

// A step under the pointer reads: another colour than the step before, and one C1 allows - white at
// an alpha over g is; a grey of its own, or the same colour, is not.
func TestButtonState(t *testing.T) {
	l := testLF(t, 2)
	btn := pixels.Rect{X: 600, Y: 10, W: 20, H: 20}
	shoot := func(name string, c color.NRGBA) {
		img := solid(1280, 800, tG)
		fill(img, l.px(btn), c)
		if err := pixels.WritePNG(filepath.Join(l.r.dir, name+".png"), img); err != nil {
			t.Fatal(err)
		}
	}
	shoot("rest", tG)
	shoot("hover", pixels.Tint(color.NRGBA{255, 255, 255, 255}, tG, 0.08))
	shoot("grey", color.NRGBA{0x50, 0x50, 0x50, 255})
	shoot("same", tG)
	for _, c := range []struct {
		name string
		ok   bool
	}{{"hover", true}, {"grey", false}, {"same", false}} {
		l.buttonState(c.name, "rest", btn)
		if res := lastResult(t, l.r); res.ok != c.ok {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
}

// Tab's focus outline is what changed against the dialog, mostly the accent: dark's in dark,
// light's in the light scheme; a change in another colour is no outline.
func TestGTKFocus(t *testing.T) {
	l := testLF(t, 2)
	write := func(name string, img image.Image) {
		if err := pixels.WritePNG(filepath.Join(l.r.dir, name+".png"), img); err != nil {
			t.Fatal(err)
		}
	}
	ring := func(c color.NRGBA) *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, pixels.Rect{X: 400, Y: 300, W: 200, H: 4}, c)
		fill(img, pixels.Rect{X: 400, Y: 380, W: 200, H: 4}, c)
		fill(img, pixels.Rect{X: 400, Y: 300, W: 4, H: 84}, c)
		fill(img, pixels.Rect{X: 596, Y: 300, W: 4, H: 84}, c)
		return img
	}
	write("gtk-dialog", solid(1280, 800, tG))
	write("gtk-dialog-light", solid(1280, 800, tG))
	l.gtkFocus("gtk-focus", ring(tAccent))
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("dark's outline: %+v", res)
	}
	l.gtkFocus("gtk-focus-light", ring(color.NRGBA{0x00, 0x7A, 0x85, 255}))
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("light's outline: %+v", res)
	}
	l.gtkFocus("gtk-focus", ring(color.NRGBA{0xE6, 0xED, 0xF2, 255}))
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("a change that is no outline passed: %+v", res)
	}
	// The focus moves: the outline leaves one control as it reaches another.
	moved := ring(tAccent)
	fill(moved, pixels.Rect{X: 400, Y: 500, W: 200, H: 4}, tAccent)
	write("gtk-dialog", moved)
	l.gtkFocus("gtk-focus", func() *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, pixels.Rect{X: 400, Y: 500, W: 200, H: 4}, tAccent)
		fill(img, pixels.Rect{X: 700, Y: 300, W: 200, H: 4}, tAccent)
		fill(img, pixels.Rect{X: 700, Y: 380, W: 200, H: 4}, tAccent)
		fill(img, pixels.Rect{X: 700, Y: 300, W: 4, H: 84}, tAccent)
		fill(img, pixels.Rect{X: 896, Y: 300, W: 4, H: 84}, tAccent)
		return img
	}())
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("an outline that moved: %+v", res)
	}
	// The outline only leaves: Tab drew none.
	l.gtkFocus("gtk-focus", func() *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, pixels.Rect{X: 400, Y: 500, W: 200, H: 4}, tAccent)
		return img
	}())
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("an outline that only left passed: %+v", res)
	}
}

// A light window draws the light accent; dark's cyan inside it fails, the dock's dot outside it does
// not.
func TestLightAccent(t *testing.T) {
	l := testLF(t, 2)
	img := solid(1280, 800, color.NRGBA{0xDE, 0xE4, 0xE8, 255})
	win := pixels.Rect{X: 100, Y: 50, W: 400, H: 300}
	fill(img, l.px(win), color.NRGBA{0xF4, 0xF6, 0xF8, 255})
	fill(img, pixels.Rect{X: 300, Y: 200, W: 40, H: 40}, color.NRGBA{0x00, 0x7A, 0x85, 255}) // a light switch's track
	fill(img, pixels.Rect{X: 620, Y: 780, W: 10, H: 10}, tAccent)                            // the dock's running dot
	l.lightAccent("w", img, win)
	if res := lastResult(t, l.r); res.name != "lf: accent w" || !res.ok {
		t.Errorf("light accent only: %+v", res)
	}
	fill(img, pixels.Rect{X: 400, Y: 300, W: 6, H: 6}, tAccent)
	l.lightAccent("w", img, win)
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("dark's accent in the window passed: %+v", res)
	}
}

// The lock prompt's error: a message shown passes, none fails, and a measurement that failed fails.
func TestLockErrorCheck(t *testing.T) {
	l := testLF(t, 2)
	verdict := func(seen lockErrorSeen) bool {
		l.r.results = nil
		l.lockErrorCheck(seen)
		for _, res := range l.r.results {
			if res.name == "lf: error lock prompt" {
				return res.ok
			}
		}
		t.Fatalf("no error check in %+v", l.r.results)
		return false
	}
	if !verdict(lockErrorSeen{Message: &lfLook{Mapped: true, Text: "Sorry, that didn't work."}}) {
		t.Error("a shown message failed")
	}
	if verdict(lockErrorSeen{Message: &lfLook{Mapped: false}}) {
		t.Error("no message passed")
	}
	if verdict(lockErrorSeen{err: errors.New("no shell")}) {
		t.Error("a failed measurement passed")
	}
}

// A screenshot of one colour drew nothing: the view fails as undrawn and nothing on it is checked
// - the boot's black would otherwise pass the overview's ground and every "none of" check.
func TestABlankScreenIsNotMeasured(t *testing.T) {
	l := testLF(t, 2)
	if err := pixels.WritePNG(filepath.Join(l.r.dir, "shell-app-grid.png"), solid(1280, 800, color.NRGBA{0, 0, 0, 255})); err != nil {
		t.Fatal(err)
	}
	l.r.results = nil
	l.view("shell-app-grid")
	if len(l.r.results) != 1 || l.r.results[0].ok || l.r.results[0].name != "lf: drawn shell-app-grid" {
		t.Errorf("%+v", l.r.results)
	}
}

// resultNamed is the last result of the check called name, failing the test without one.
func resultNamed(t *testing.T, r *report, name string) result {
	t.Helper()
	for i := len(r.results) - 1; i >= 0; i-- {
		if r.results[i].name == name {
			return r.results[i]
		}
	}
	t.Fatalf("no %q in %+v", name, r.results)
	return result{}
}

// The lock screen: its ground the boot's black, the dark shell theme loaded, and the clock's digits
// regular - a bold clock's stems are past the ratio.
func TestLockView(t *testing.T) {
	l := testLF(t, 1)
	fg := color.NRGBA{0xE6, 0xED, 0xF2, 255}
	clock := pixels.Rect{X: 200, Y: 100, W: 240, H: 80}
	shot := func(stem int) image.Image {
		img := solid(640, 400, tBlack)
		fill(img, pixels.Rect{X: 220, Y: 110, W: stem, H: 60}, fg)
		fill(img, pixels.Rect{X: 260, Y: 110, W: stem, H: 60}, fg)
		return img
	}
	s := lockSeen{Group: &lfLook{BG: "#000000FF"}, Time: &lfLook{X: clock.X, Y: clock.Y, W: clock.W, H: clock.H, FG: "#E6EDF2FF"},
		URI: "resource:///org/gnome/shell/theme/gnome-shell-dark.css"}
	l.lock(shot(6), s)
	for _, n := range []string{"lf: ground lock", "lf: theme lock", "lf: type lock clock"} {
		if res := resultNamed(t, l.r, n); !res.ok {
			t.Errorf("%s: %+v", n, res)
		}
	}
	l.lock(shot(14), s)
	if res := resultNamed(t, l.r, "lf: type lock clock"); res.ok {
		t.Errorf("a bold clock passed: %+v", res)
	}
	s.URI = "resource:///org/gnome/shell/theme/gnome-shell.css"
	l.lock(shot(6), s)
	if res := resultNamed(t, l.r, "lf: theme lock"); res.ok {
		t.Errorf("the stock theme passed: %+v", res)
	}
}

// The installer's window: its body f, the border outside its left and bottom edges, its headerbar
// g, and one line of the border under the headerbar. Without the ring, or with another window
// focused, it fails.
func TestInstallerView(t *testing.T) {
	l := testLF(t, 1)
	win := pixels.Rect{X: 100, Y: 40, W: 440, H: 320}
	f, g, border := l.c("neutral-f"), l.c("neutral-g"), l.c("dark-border")
	shot := func(ring bool) image.Image {
		img := solid(640, 400, l.c("neutral-e"))
		fill(img, win, f)
		fill(img, pixels.Rect{X: win.X, Y: win.Y, W: win.W, H: 46}, g)
		fill(img, pixels.Rect{X: win.X, Y: win.Y + 46, W: win.W, H: 1}, border)
		if ring {
			fill(img, pixels.Rect{X: win.X - 1, Y: win.Y, W: 1, H: win.H}, border)
			fill(img, pixels.Rect{X: win.X, Y: win.Y + win.H, W: win.W, H: 1}, border)
		}
		return img
	}
	s := windowSeen{Win: &win, Cls: "io.github.construct_linux.Installer"}
	l.installer(shot(true), s)
	for _, n := range []string{"lf: window installer", "lf: rule installer headerbar"} {
		if res := resultNamed(t, l.r, n); !res.ok {
			t.Errorf("%s: %+v", n, res)
		}
	}
	l.installer(shot(false), s)
	if res := resultNamed(t, l.r, "lf: window installer"); res.ok {
		t.Errorf("no ring passed: %+v", res)
	}
	l.installer(shot(true), windowSeen{Win: &win, Cls: "org.gnome.Nautilus"})
	if res := resultNamed(t, l.r, "lf: window installer"); res.ok {
		t.Errorf("another window passed: %+v", res)
	}
}

// Each forced state fills its toggle with the accent at its step's tint over h, in St and on the
// screen; a step not forced, or a fill another step's, fails.
func TestStatesView(t *testing.T) {
	l := testLF(t, 1)
	h, accent := l.c("neutral-h"), l.c("dark-accent")
	img := solid(640, 400, h)
	var s []stateSeen
	for i, st := range []struct{ state, tok string }{{"rest", "tint-dark-rest"}, {"hover", "tint-dark-hover"}, {"active", "tint-dark-active"}} {
		want := pixels.Tint(accent, h, l.pal.nums[st.tok])
		r := pixels.Rect{X: 20 + 120*i, Y: 20, W: 100, H: 40}
		fill(img, r, want)
		look := &lfLook{X: r.X, Y: r.Y, W: r.W, H: r.H, Chain: []string{pixels.Hexa(want)}}
		title := &lfLook{FG: pixels.Hexa(l.c("dark-accent-text")), Opacity: 1, Chain: []string{pixels.Hexa(want)}}
		s = append(s, stateSeen{State: st.state, Look: look, Title: title})
	}
	l.states(img, s)
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("three states as the tints: %+v", res)
	}
	l.states(img, s[:2])
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("active not forced passed: %+v", res)
	}
	s[1].Look.Chain = s[0].Look.Chain
	l.states(img, s)
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("hover filled as rest passed: %+v", res)
	}
}

// Every shown app's icon is Tela's or a brand tile; one the icon theme found elsewhere fails.
func TestShellIcons(t *testing.T) {
	l := testLF(t, 1)
	s := shellSeen{ToggleOn: true, Background: "const DEFAULT_BACKGROUND_COLOR = new Cogl.Color({red: 0, green: 0, blue: 0, alpha: 255});",
		Icons: []struct{ ID, Icon, File string }{
			{"org.gnome.TextEditor.desktop", "org.gnome.TextEditor", "/usr/share/icons/Tela-dark/scalable/apps/org.gnome.TextEditor.svg"},
			{"io.github.construct_linux.Installer.desktop", "io.github.construct_linux.Installer", "/usr/share/icons/hicolor/scalable/apps/io.github.construct_linux.Installer.svg"},
		}}
	l.shell(s)
	for _, n := range []string{"lf: icons apps", "lf: gresource toggle-on", "lf: startup stage background.js"} {
		if res := resultNamed(t, l.r, n); !res.ok {
			t.Errorf("%s: %+v", n, res)
		}
	}
	if res := resultNamed(t, l.r, "lf: css shell"); res.ok {
		t.Errorf("no stylesheet passed: %+v", res)
	}
	s.Icons = append(s.Icons, struct{ ID, Icon, File string }{"x.desktop", "x", "/usr/share/icons/Adwaita/scalable/apps/x.svg"})
	l.shell(s)
	if res := resultNamed(t, l.r, "lf: icons apps"); res.ok || !strings.Contains(res.detail, "x.desktop") {
		t.Errorf("Adwaita's icon passed: %+v", res)
	}
}

// A maximized window is square and ringless; a rounded corner shows the desk in it.
func TestSquareView(t *testing.T) {
	l := testLF(t, 1)
	win := pixels.Rect{X: 0, Y: 40, W: 640, H: 320}
	img := solid(640, 400, l.c("neutral-e"))
	fill(img, win, l.c("neutral-g"))
	l.square(img, win)
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("a square window: %+v", res)
	}
	img.SetNRGBA(win.X, win.Y, l.c("neutral-e"))
	l.square(img, win)
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("a rounded corner passed: %+v", res)
	}
}
