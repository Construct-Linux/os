package main

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"
)

var (
	tInk    = color.NRGBA{0x0B, 0x11, 0x16, 255}
	tG      = color.NRGBA{0x17, 0x20, 0x28, 255}
	tH      = color.NRGBA{0x1F, 0x2A, 0x33, 255}
	tRing   = color.NRGBA{0x2A, 0x38, 0x43, 255}
	tAccent = color.NRGBA{0x00, 0xE5, 0xFF, 255}
	tBlack  = color.NRGBA{0, 0, 0, 255}
)

func fill(img *image.NRGBA, r lfRect, c color.NRGBA) {
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
	img.SetNRGBA(x, y, tint(c, at(img, x, y), a))
}

const tPalette = `{"brand-boot":"#000000","neutral-e":"#0B1116","neutral-f":"#11181E","neutral-g":"#172028",
"neutral-h":"#1F2A33","dark-border":"#2A3843","dark-accent":"#00E5FF","dark-accent-text":"#00E5FF",
"dark-text":"#E6EDF2","dark-muted":"#9FB3C0","light-accent":"#007A85","light-accent-text":"#00646D",
"tint-dark-rest":0.16,"shape-inset":6,"shape-radius-control":9,"shape-radius-surface":15,"shape-radius-sheet":24,
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

// Contrast is WCAG 2.2's, and a tint is the accent at its alpha over the surface: the figures the
// brand's palette.py computes.
func TestContrastAndTint(t *testing.T) {
	if c := contrast(tBlack, color.NRGBA{255, 255, 255, 255}); math.Abs(c-21) > 0.01 {
		t.Errorf("black on white %.2f", c)
	}
	if c := contrast(tRing, tInk); math.Abs(c-1.58) > 0.01 {
		t.Errorf("the ring on e %.2f, the plan says 1.58", c)
	}
	if c := tint(tAccent, tH, 0.16); c != (color.NRGBA{0x1A, 0x48, 0x54, 255}) {
		t.Errorf("16%% accent over h is %s, the plan says #1A4854", hexOf(c))
	}
	if c := over(color.NRGBA{255, 255, 255, 0}, tG); c != tG {
		t.Errorf("transparent over g is %s", hexOf(c))
	}
}

// A bar: g inside a 1 px ring, with text on it. Its colour is the fill, not the text's; the ring is
// found at an edge that is a pixel off the extent, and not where there is none.
func TestSurfacePixels(t *testing.T) {
	img := solid(200, 60, tInk)
	bar := lfRect{20, 10, 160, 36}
	fill(img, bar, tRing)
	fill(img, bar.inset(1, 1), tG)
	fill(img, lfRect{60, 20, 40, 12}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	if c, share := modeColor(img, bar.inset(3, 3)); c != tG || share < 0.5 {
		t.Errorf("mode %s over %.2f", hexOf(c), share)
	}
	// The extent says y 9, the ring is at 10.
	if p, ok := edgeHit(img, 100, 9, 0, -1, lfRingReach, tRing, lfRingTol); !ok || p.Y != 10 {
		t.Errorf("top ring: %v %v", p, ok)
	}
	if _, ok := edgeHit(img, 100, 5, 0, -1, lfRingReach, tRing, lfRingTol); ok {
		t.Error("a ring 5 px away was taken for this edge's")
	}
	if n := countNear(img, lfRect{0, 0, 200, 60}, tRing, 0); n != 2*160+2*34 {
		t.Errorf("%d ring pixels", n)
	}
}

// Today's ring: a 2 px accent ring inside a circle is most of its outline; the same day filled
// with the surface is none of it.
func TestRingShare(t *testing.T) {
	img := solid(60, 60, tH)
	day := lfRect{12, 12, 36, 36}
	for y := range 60 {
		for x := range 60 {
			ring := coverage(x, y, func(fx, fy float64) bool {
				d := math.Hypot(fx-30, fy-30)
				return d <= 18 && d >= 16
			})
			blendAt(img, x, y, tAccent, ring)
		}
	}
	if s := ringShare(img, day, 1, tAccent, lfRingAccentTol); s < 0.9 {
		t.Errorf("a full ring: %.2f", s)
	}
	if s := ringShare(solid(60, 60, tH), day, 1, tAccent, lfRingAccentTol); s != 0 {
		t.Errorf("no ring: %.2f", s)
	}
	// A pill: its top side is a quarter of its outline (60 px of sides, 2 ends of radius 19).
	wide := solid(100, 40, tH)
	fill(wide, lfRect{20, 0, 60, 2}, tAccent)
	if s := ringShare(wide, lfRect{0, 0, 100, 40}, 1, tAccent, lfRingAccentTol); s < 0.22 || s > 0.28 {
		t.Errorf("one straight side of a pill: %.2f", s)
	}
}

// A window's corner, antialiased as a renderer draws it, fits its radius within the check's
// pixel: libadwaita's 15 and a control's 9.
func TestCornerRadius(t *testing.T) {
	for _, r := range []float64{9, 15, 18} {
		img := solid(120, 80, color.NRGBA{0xD0, 0xD2, 0xD4, 255})
		win := color.NRGBA{0xF4, 0xF6, 0xF8, 255}
		for y := 10; y < 80; y++ {
			for x := 10; x < 120; x++ {
				a := coverage(x, y, func(fx, fy float64) bool {
					cx, cy := 10+r, 10+r
					if fx < cx && fy < cy {
						return math.Hypot(fx-cx, fy-cy) <= r
					}
					return true
				})
				blendAt(img, x, y, win, a)
			}
		}
		got, err := cornerRadius(img, 10, 10, 30)
		if err != nil || math.Abs(got-r) > lfRadiusTol {
			t.Errorf("radius %v fits %.2f (%v)", r, got, err)
		}
	}
	if _, err := cornerRadius(solid(80, 80, tG), 10, 10, 30); err == nil {
		t.Error("a window the colour of what is around it: no error")
	}
}

// A light slab is one big light area; glyphs are many small ones.
func TestLightSlab(t *testing.T) {
	img := solid(200, 100, tH)
	for i := range 10 {
		fill(img, lfRect{10 + 15*i, 10, 8, 12}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	}
	if n := lightSlab(img, lfRect{0, 0, 200, 100}, lfSlabLightness); n != 96 {
		t.Errorf("glyphs: largest %d", n)
	}
	fill(img, lfRect{20, 40, 30, 30}, color.NRGBA{0xF4, 0xF6, 0xF8, 255})
	if n := lightSlab(img, lfRect{0, 0, 200, 100}, lfSlabLightness); n != 900 {
		t.Errorf("a slab: largest %d", n)
	}
}

// Stem over cap height: vertical strokes measure it, a crossbar does not.
func TestStrokeRatio(t *testing.T) {
	for _, stem := range []int{8, 14} {
		img := solid(200, 120, tBlack)
		fg := color.NRGBA{0xE6, 0xED, 0xF2, 255}
		// An H and an I, 70 px tall.
		fill(img, lfRect{20, 20, stem, 70}, fg)
		fill(img, lfRect{60, 20, stem, 70}, fg)
		fill(img, lfRect{20, 50, 40 + stem, stem}, fg)
		fill(img, lfRect{100, 20, stem, 70}, fg)
		got, err := strokeRatio(img, lfRect{0, 0, 200, 120}, fg, tBlack)
		if want := float64(stem) / 70; err != nil || math.Abs(got-want) > 0.001 {
			t.Errorf("stem %d: %.3f, want %.3f (%v)", stem, got, want, err)
		}
	}
	if _, err := strokeRatio(solid(50, 50, tBlack), lfRect{0, 0, 50, 50}, tAccent, tBlack); err == nil {
		t.Error("no ink: no error")
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
	fill(menu, lfRect{100, 90, 200, 16}, color.NRGBA{0x98, 0x98, 0x98, 255})
	if f := filmStats(rgba(menu)); !f.menu || f.plymouth() {
		t.Errorf("menu: %+v", f)
	}
	logo := solid(400, 200, tBlack)
	fill(logo, lfRect{100, 90, 40, 40}, color.NRGBA{0x30, 0x60, 0xC0, 255})
	fill(logo, lfRect{150, 90, 60, 10}, color.NRGBA{0xF0, 0xF8, 0xFF, 255})
	if f := filmStats(rgba(logo)); f.cyan != 0 || f.menu {
		t.Errorf("the firmware's logo: %+v", f)
	}
	ply := solid(400, 200, tBlack)
	fill(ply, lfRect{150, 40, 100, 2}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	fill(ply, lfRect{150, 140, 100, 2}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	fill(ply, lfRect{150, 40, 2, 100}, color.NRGBA{0x40, 0xF0, 0xFF, 255})
	f := filmStats(rgba(ply))
	if !f.plymouth() || f.mark != (lfRect{150, 40, 100, 102}) {
		t.Errorf("Plymouth: %+v", f)
	}
	ground := solid(400, 200, tBlack)
	fill(ground, lfRect{0, 0, 400, 120}, color.NRGBA{0x28, 0x28, 0x28, 255})
	if f := filmStats(rgba(ground)); math.Abs(f.cover-0.6) > 1e-9 || f.coverHex != "#282828" {
		t.Errorf("SystemBackground: %+v", f)
	}
}

// The film's report: a wrong ground after Plymouth fails it, one before does not; the menu's time
// on screen is reported; the mark is where Plymouth drew it whole.
func TestFilmReport(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	whole := lfRect{100, 100, 200, 200}
	frames := []filmFrame{
		{at: ms(0), black: 1},
		{at: ms(100), black: 0.99, menu: true},
		{at: ms(1500), black: 0.99, menu: true},
		{at: ms(1600), cover: 0.9, coverHex: "#282828"},
		{at: ms(2000), black: 0.95, cyan: 500, mark: lfRect{100, 200, 200, 100}},
		{at: ms(2100), black: 0.95, cyan: 900, mark: whole},
		{at: ms(2200), black: 0.95, cyan: 900, mark: whole},
		{at: ms(3000), cover: 0.3, coverHex: "#11181E"},
	}
	r := &report{dir: t.TempDir()}
	mark := r.filmReport(frames, 0)
	if mark == nil || mark[0] != 200 || mark[1] != 200 {
		t.Errorf("mark %v", mark)
	}
	got := map[string]result{}
	for _, x := range r.results {
		got[x.name] = x
	}
	if g := got["boot film: ground"]; !g.ok || !strings.Contains(g.detail, "#11181E 30%") {
		t.Errorf("ground: %+v", g)
	}
	if m := got["boot film: systemd-boot menu"]; !strings.Contains(m.detail, "on screen 1.5s") {
		t.Errorf("menu: %+v", m)
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
	pill := lfRect{480, 594, 320, 36}
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
	fill(prompt, lfRect{500, 140, 280, 3}, tAccent)
	fill(after, lfRect{500, 140, 280, 3}, tAccent)
	fill(after, lfRect{500, 240, 280, 3}, tAccent)
	if ok, detail := gridBack(prompt, after, 594); !ok {
		t.Error(detail)
	}
	if ok, _ := gridBack(prompt, prompt, 594); ok {
		t.Error("the outro's last frame again passed as the grid back")
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
	rules := parseCSS(tCSS)
	if len(rules) != 14 {
		t.Fatalf("%d rules", len(rules))
	}
	if m, _ := cssSubject(rules, ".osd-window"); m["background-image"] != `url("assets/a;b.svg")` {
		t.Errorf("a ; inside url() split the declaration: %q", m["background-image"])
	}
	if m, _ := cssSubject(rules, ".popup-menu-content"); m["background-color"] != "#1f2a33" {
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
	if ok || !strings.Contains(detail, "3 rules") || !strings.Contains(detail, ".quick-toggle:focus") || strings.Contains(detail, "calendar-day") ||
		strings.Contains(detail, "pager-button") || strings.Contains(detail, "clock-display") {
		t.Errorf("G2: %v %s", ok, detail)
	}

	ok, detail = cssPalette(rules, p)
	if ok || !strings.Contains(detail, "#3584E4") || !strings.Contains(detail, "#282828 (grey)") || strings.Contains(detail, "#FFFFFF") {
		t.Errorf("G3: %v %s", ok, detail)
	}
	if ok, detail := cssPalette(parseCSS(`#panel { color: rgba(230, 237, 242, 0.08); border-color: transparent; }`), p); !ok {
		t.Errorf("G3, a palette colour at an alpha: %s", detail)
	}

	ok, detail = cssWeights(rules)
	if ok || !strings.Contains(detail, ".dash-label: 800") || strings.Contains(detail, "#panel") {
		t.Errorf("G6: %v %s", ok, detail)
	}
}
