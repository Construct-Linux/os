package main

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	if p, ok := ringHit(img, 100, 9, 0, -1, lfRingReach, tRing); !ok || p.Y != 10 {
		t.Errorf("top ring: %v %v", p, ok)
	}
	if _, ok := ringHit(img, 100, 5, 0, -1, lfRingReach, tRing); ok {
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
	if s := ringShare(img, day, 1, tAccent); s < 0.9 {
		t.Errorf("a full ring: %.2f", s)
	}
	if s := ringShare(solid(60, 60, tH), day, 1, tAccent); s != 0 {
		t.Errorf("no ring: %.2f", s)
	}
	// A pill: its top side is a quarter of its outline (60 px of sides, 2 ends of radius 19).
	wide := solid(100, 40, tH)
	fill(wide, lfRect{20, 0, 60, 2}, tAccent)
	if s := ringShare(wide, lfRect{0, 0, 100, 40}, 1, tAccent); s < 0.22 || s > 0.28 {
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
		{at: ms(2000), black: 0.95, cyan: 500, w: 400, h: 400, mark: lfRect{100, 200, 200, 100}},
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

// The grid after the password is its own result: a screen that cannot be read, or none at all, is
// no grid, and one readable screen with the grid back is.
func TestGridPlymouth(t *testing.T) {
	dir := t.TempDir()
	prompt := solid(1280, 800, tBlack)
	fill(prompt, lfRect{500, 140, 280, 3}, tAccent)
	back := solid(1280, 800, tBlack)
	fill(back, lfRect{500, 140, 280, 3}, tAccent)
	fill(back, lfRect{500, 240, 280, 3}, tAccent)
	same, grid, broken := filepath.Join(dir, "same.png"), filepath.Join(dir, "grid.png"), filepath.Join(dir, "broken.png")
	if writePNG(same, prompt) != nil || writePNG(grid, back) != nil || os.WriteFile(broken, []byte("\x89PNG partial"), 0o644) != nil {
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
	if ok || !strings.Contains(detail, "3 rules") || !strings.Contains(detail, ".quick-toggle:focus") || !strings.Contains(detail, ".calendar-day:focus") ||
		strings.Contains(detail, "popup-menu-item") ||
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

// A pill's end over the light desk: its apex pixel is the ring antialiased against the desk, a
// blend the plain tolerance refuses; ringHit takes it, and not the desk alone.
func TestRingHitBlend(t *testing.T) {
	desk := color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	img := solid(20, 10, desk)
	fill(img, lfRect{6, 0, 14, 10}, tG)
	img.SetNRGBA(6, 5, tint(tRing, desk, 0.975))
	if _, ok := ringHit(img, 6, 5, -1, 0, lfRingReach, tRing); !ok {
		t.Errorf("the apex %s was not taken for the ring", hexOf(at(img, 6, 5)))
	}
	img.SetNRGBA(6, 5, tint(tRing, desk, 0.5))
	if _, ok := ringHit(img, 6, 5, -1, 0, lfRingReach, tRing); ok {
		t.Error("half a ring was taken for one")
	}
	if a, res := coverOf(tint(tAccent, tH, 0.18), tAccent, tH); math.Abs(a-0.18) > 0.01 || res > 1 {
		t.Errorf("the rest tint read as %.3f, %d off", a, res)
	}
}

// Ghostty's stock background as a surface counts; the same colour on single antialiased pixels
// does not.
func TestFlatCount(t *testing.T) {
	stock := color.NRGBA{0x28, 0x2C, 0x34, 255}
	img := solid(40, 40, tG)
	for i := range 10 {
		img.SetNRGBA(3+3*i, 5, stock)
	}
	if n := flatCount(img, lfRect{0, 0, 40, 40}, stock, lfSurfaceTol); n != 0 {
		t.Errorf("antialiasing counted: %d", n)
	}
	fill(img, lfRect{10, 20, 10, 10}, stock)
	if n := flatCount(img, lfRect{0, 0, 40, 40}, stock, lfSurfaceTol); n != 64 {
		t.Errorf("a 10x10 surface has 64 inner pixels, counted %d", n)
	}
}

// Marks are not areas: text-thin strokes, a 2 px ring and a 24 px disc stay marks; a solid button
// and a 6 px bar are areas larger than a mark.
func TestSolidAreas(t *testing.T) {
	img := solid(400, 200, tH)
	acc := []color.NRGBA{tAccent}
	all := lfRect{0, 0, 400, 200}
	fill(img, lfRect{10, 10, 200, 3}, tAccent) // a slider's fill
	fill(img, lfRect{10, 30, 4, 60}, tAccent)  // a 4 px stroke
	fill(img, lfRect{50, 50, 24, 24}, tAccent) // the disc's box
	fill(img, lfRect{100, 40, 40, 2}, tAccent) // a ring's side
	if a := solidAreas(img, all, acc, 1); len(a) != 0 {
		t.Errorf("marks taken for areas: %v", a)
	}
	fill(img, lfRect{200, 100, 120, 36}, tAccent)
	fill(img, lfRect{20, 150, 300, 6}, tAccent)
	a := solidAreas(img, all, acc, 1)
	if len(a) != 2 || a[0] != (lfRect{20, 150, 300, 6}) && a[1] != (lfRect{20, 150, 300, 6}) {
		t.Errorf("a button and a bar: %v", a)
	}
	if s := accentShare(img, all, acc, lfSweepTol); s < 0.05 || s > 0.1 {
		t.Errorf("share %.3f", s)
	}
	// Tela's teal in the dock is not light's accent.
	if s := accentShare(solid(10, 10, color.NRGBA{0x00, 0x82, 0x92, 255}), lfRect{0, 0, 10, 10}, []color.NRGBA{{0x00, 0x7A, 0x85, 255}}, lfSweepTol); s != 0 {
		t.Errorf("Tela's teal counted: %.2f", s)
	}
}

// A line across a pane is found where it is, past text on the rows above it; a column of icons
// is no separator, the line between two panes is.
func TestRules(t *testing.T) {
	shelf, c := color.NRGBA{0xE9, 0xED, 0xF0, 255}, color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	line := color.NRGBA{0xCF, 0xD5, 0xD9, 255}
	img := solid(300, 200, c)
	fill(img, lfRect{0, 0, 100, 200}, shelf)
	fill(img, lfRect{100, 0, 1, 200}, line)
	fill(img, lfRect{0, 47, 300, 1}, line)
	fill(img, lfRect{140, 20, 60, 10}, tInk) // a title
	for y := 60; y < 200; y += 20 {
		fill(img, lfRect{20, y, 16, 16}, tInk) // the sidebar's icons
	}
	if y, col, ok := hRule(img, 110, 290, 8, 80); !ok || y != 47 || col != line {
		t.Errorf("content's rule: %d %s %v", y, hexOf(col), ok)
	}
	if y, _, ok := hRule(img, 10, 90, 8, 80); !ok || y != 47 {
		t.Errorf("sidebar's rule: %d %v", y, ok)
	}
	if x, _, ok := vRule(img, 10, 150, 60, 190, 1); !ok || x != 100 {
		t.Errorf("separator: %d %v", x, ok)
	}
	if _, _, ok := hRule(solid(100, 100, c), 0, 100, 8, 80); ok {
		t.Error("a rule found on a bare pane")
	}
}

// C1 as G3 reads it: the accent and white at an alpha over a surface, written opaque, are the
// palette's; a mix of the accent and the error colour, or a grey, are not.
func TestComposite(t *testing.T) {
	p, err := parseLFPalette([]byte(tPalette))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []color.NRGBA{tint(tAccent, tH, 0.5), tint(color.NRGBA{255, 255, 255, 255}, tH, 0.06), tint(tInk, tAccent, 0.08)} {
		if !p.composite(c) {
			t.Errorf("%s is a composite", hexOf(c))
		}
	}
	for _, c := range []color.NRGBA{{0x28, 0x28, 0x28, 255}, {0xE3, 0x59, 0x84, 255}, {0x2D, 0x46, 0x5A, 255}} {
		if p.composite(c) {
			t.Errorf("%s taken for a composite", hexOf(c))
		}
	}
}

// C8: a ring of the accent is focus's; decoration's 1 px edge on a tint and the Alt+Esc frame are
// not rings to hold.
func TestAccentRings(t *testing.T) {
	css := `
.button:focus { box-shadow: inset 0 0 0 2px #00E5FF; }
.calendar-day:selected:focus { box-shadow: inset 0 0 0 2px #00E5FF !important; }
.calendar-today { box-shadow: inset 0 0 0 2px #00E5FF; }
.workspace-thumbnail-indicator { border: 2px solid #00E5FF; border-radius: 3px; }
.tile-preview { background-color: rgba(0, 229, 255, 0.08); border: 1px solid #00E5FF; border-radius: 9px; }
.cycler-highlight { border: 5px solid #00E5FF; }
.slider { box-shadow: 0 2px 4px #00E5FF; }
.quick-toggle:checked .quick-toggle-icon { background-color: #00E5FF; }
.magnifier-zoom-region.full-screen { border-width: 0; }`
	ok, detail := cssAccentRings(parseCSS(css), tAccent)
	if ok || !strings.Contains(detail, ".calendar-today") || !strings.Contains(detail, ".workspace-thumbnail-indicator") ||
		strings.Contains(detail, ".button") || strings.Contains(detail, ":selected") || strings.Contains(detail, "tile-preview") || strings.Contains(detail, "cycler") ||
		strings.Contains(detail, ".slider") || strings.Contains(detail, "quick-toggle") || strings.Contains(detail, "magnifier") {
		t.Errorf("C8: %v %s", ok, detail)
	}
}

// GTK's CSS keeps most of a scheme's rules in @media blocks: they are rules of the sheet.
func TestParseCSSMedia(t *testing.T) {
	rules := parseCSS(`@import url("a.css");
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

// At 2x a mark is twice the pixels and still a mark; an area is an area at any scale.
func TestSolidAreasScaled(t *testing.T) {
	img := solid(800, 400, tH)
	acc := []color.NRGBA{tAccent}
	all := lfRect{0, 0, 800, 400}
	fill(img, lfRect{20, 60, 8, 120}, tAccent)   // a 4 px stroke, at 2x
	fill(img, lfRect{100, 100, 48, 48}, tAccent) // the 24 px disc's box, at 2x
	if a := solidAreas(img, all, acc, 2); len(a) != 0 {
		t.Errorf("marks at 2x taken for areas: %v", a)
	}
	if a := solidAreas(img, all, acc, 1); len(a) != 2 {
		t.Errorf("the same pixels at 1x are areas: %v", a)
	}
	fill(img, lfRect{400, 200, 240, 72}, tAccent)
	if a := solidAreas(img, all, acc, 2); len(a) != 1 {
		t.Errorf("a button at 2x: %v", a)
	}
}

// A separator a stage pixel wide is two physical ones at 2x: found with its width, missed without.
func TestVRuleScaled(t *testing.T) {
	shelf, c := color.NRGBA{0xE9, 0xED, 0xF0, 255}, color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	line := color.NRGBA{0xCF, 0xD5, 0xD9, 255}
	img := solid(600, 400, c)
	fill(img, lfRect{0, 0, 200, 400}, shelf)
	fill(img, lfRect{200, 0, 2, 400}, line)
	if x, col, ok := vRule(img, 20, 300, 120, 380, 2); !ok || x != 200 || col != line {
		t.Errorf("2x separator: %d %v", x, ok)
	}
	if _, _, ok := vRule(img, 20, 300, 120, 380, 1); ok {
		t.Error("a 2 px line read as 1 px wide")
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
		{at: ms(1000), black: 0.95, cyan: 900, w: 1280, h: 800, mark: lfRect{540, 240, 200, 200}},
		{at: ms(1100), black: 0.95, cyan: 900, w: 1280, h: 800, mark: lfRect{540, 240, 200, 200}},
		{at: ms(2000), black: 0.95, cyan: 3600, w: 2880, h: 1800, mark: lfRect{1240, 600, 400, 400}},
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
	l.r.views = map[string]*needleSpec{"shell-calendar": {}}
	l.cur = "shell-calendar"
	l.check("today", "calendar", true, "")
	l.check("focus", "calendar day", false, "")
	l.cur = ""
	l.check("fonts", "G4", false, "")
	got := l.r.views["shell-calendar"].LF
	if got.Pass != 1 || got.Fail != 1 || !slices.Equal(got.Checks, []string{"focus calendar day"}) {
		t.Errorf("counted %+v", got)
	}
	l.r.viewSweep("shell-calendar", []lfRect{{1, 2, 30, 40}}, 0.004)
	if v := l.r.lookViewOf("shell-calendar"); len(v.Sweep.Areas) != 1 || v.Sweep.Areas[0].W != 30 || v.LF.Fail != 1 {
		t.Errorf("look.json's view %+v", v)
	}
}

// A step under the pointer reads: another colour than the step before, and one C1 allows - white at
// an alpha over g is; a grey of its own, or the same colour, is not.
func TestButtonState(t *testing.T) {
	l := testLF(t, 2)
	btn := lfRect{600, 10, 20, 20}
	shoot := func(name string, c color.NRGBA) {
		img := solid(1280, 800, tG)
		fill(img, l.px(btn), c)
		if err := writePNG(filepath.Join(l.r.dir, name+".png"), img); err != nil {
			t.Fatal(err)
		}
	}
	shoot("rest", tG)
	shoot("hover", tint(color.NRGBA{255, 255, 255, 255}, tG, 0.08))
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
		if err := writePNG(filepath.Join(l.r.dir, name+".png"), img); err != nil {
			t.Fatal(err)
		}
	}
	ring := func(c color.NRGBA) *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, lfRect{400, 300, 200, 4}, c)
		fill(img, lfRect{400, 380, 200, 4}, c)
		fill(img, lfRect{400, 300, 4, 84}, c)
		fill(img, lfRect{596, 300, 4, 84}, c)
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
	fill(moved, lfRect{400, 500, 200, 4}, tAccent)
	write("gtk-dialog", moved)
	l.gtkFocus("gtk-focus", func() *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, lfRect{400, 500, 200, 4}, tAccent)
		fill(img, lfRect{700, 300, 200, 4}, tAccent)
		fill(img, lfRect{700, 380, 200, 4}, tAccent)
		fill(img, lfRect{700, 300, 4, 84}, tAccent)
		fill(img, lfRect{896, 300, 4, 84}, tAccent)
		return img
	}())
	if res := lastResult(t, l.r); !res.ok {
		t.Errorf("an outline that moved: %+v", res)
	}
	// The outline only leaves: Tab drew none.
	l.gtkFocus("gtk-focus", func() *image.NRGBA {
		img := solid(1280, 800, tG)
		fill(img, lfRect{400, 500, 200, 4}, tAccent)
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
	win := lfRect{100, 50, 400, 300}
	fill(img, l.px(win), color.NRGBA{0xF4, 0xF6, 0xF8, 255})
	fill(img, lfRect{300, 200, 40, 40}, color.NRGBA{0x00, 0x7A, 0x85, 255}) // a light switch's track
	fill(img, lfRect{620, 780, 10, 10}, tAccent)                            // the dock's running dot
	l.lightAccent("w", img, win)
	if res := lastResult(t, l.r); res.name != "lf: accent w" || !res.ok {
		t.Errorf("light accent only: %+v", res)
	}
	fill(img, lfRect{400, 300, 6, 6}, tAccent)
	l.lightAccent("w", img, win)
	if res := lastResult(t, l.r); res.ok {
		t.Errorf("dark's accent in the window passed: %+v", res)
	}
}
