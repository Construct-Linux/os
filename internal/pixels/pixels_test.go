package pixels

import (
	"image"
	"image/color"
	"math"
	"testing"
)

var (
	tInk    = color.NRGBA{0x0B, 0x11, 0x16, 255}
	tG      = color.NRGBA{0x17, 0x20, 0x28, 255}
	tH      = color.NRGBA{0x1F, 0x2A, 0x33, 255}
	tRing   = color.NRGBA{0x2A, 0x38, 0x43, 255}
	tAccent = color.NRGBA{0x00, 0xE5, 0xFF, 255}
	tBlack  = color.NRGBA{0, 0, 0, 255}
)

// The values the harness's look checks measure with (cmd/image lf.go), so these tests hold the
// measurements where they are used.
const (
	tReach         = 2
	tRadiusTol     = 1
	tSlabLightness = 0.85
	tSurfaceTol    = 2
	tSweepTol      = 4
	tMarkStroke    = 4
	tMarkDisc      = 24
	tRingAccentTol = 40
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func fill(img *image.NRGBA, r Rect, c color.NRGBA) {
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
	img.SetNRGBA(x, y, Tint(c, At(img, x, y), a))
}

// Contrast is WCAG 2.2's, and a tint is the accent at its alpha over the surface: the figures the
// brand's palette.py computes.
func TestContrastAndTint(t *testing.T) {
	if c := Contrast(tBlack, color.NRGBA{255, 255, 255, 255}); math.Abs(c-21) > 0.01 {
		t.Errorf("black on white %.2f", c)
	}
	if c := Contrast(tRing, tInk); math.Abs(c-1.58) > 0.01 {
		t.Errorf("the ring on e %.2f, the plan says 1.58", c)
	}
	if c := Tint(tAccent, tH, 0.16); c != (color.NRGBA{0x1A, 0x48, 0x54, 255}) {
		t.Errorf("16%% accent over h is %s, the plan says #1A4854", Hex(c))
	}
	if c := Over(color.NRGBA{255, 255, 255, 0}, tG); c != tG {
		t.Errorf("transparent over g is %s", Hex(c))
	}
}

// A bar: g inside a 1 px ring, with text on it. Its colour is the fill, not the text's; the ring is
// found at an edge that is a pixel off the extent, and not where there is none.
func TestSurfacePixels(t *testing.T) {
	img := solid(200, 60, tInk)
	bar := Rect{20, 10, 160, 36}
	fill(img, bar, tRing)
	fill(img, bar.Inset(1, 1), tG)
	fill(img, Rect{60, 20, 40, 12}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	if c, share := ModeColor(img, bar.Inset(3, 3)); c != tG || share < 0.5 {
		t.Errorf("mode %s over %.2f", Hex(c), share)
	}
	// The extent says y 9, the ring is at 10.
	if p, ok := RingHit(img, 100, 9, 0, -1, tReach, tRing); !ok || p.Y != 10 {
		t.Errorf("top ring: %v %v", p, ok)
	}
	if _, ok := RingHit(img, 100, 5, 0, -1, tReach, tRing); ok {
		t.Error("a ring 5 px away was taken for this edge's")
	}
	if n := CountNear(img, Rect{0, 0, 200, 60}, tRing, 0); n != 2*160+2*34 {
		t.Errorf("%d ring pixels", n)
	}
}

// Today's ring: a 2 px accent ring inside a circle is most of its outline; the same day filled
// with the surface is none of it.
func TestRingShare(t *testing.T) {
	img := solid(60, 60, tH)
	day := Rect{12, 12, 36, 36}
	for y := range 60 {
		for x := range 60 {
			ring := coverage(x, y, func(fx, fy float64) bool {
				d := math.Hypot(fx-30, fy-30)
				return d <= 18 && d >= 16
			})
			blendAt(img, x, y, tAccent, ring)
		}
	}
	if s := RingShare(img, day, 1, tAccent, tRingAccentTol); s < 0.9 {
		t.Errorf("a full ring: %.2f", s)
	}
	if s := RingShare(solid(60, 60, tH), day, 1, tAccent, tRingAccentTol); s != 0 {
		t.Errorf("no ring: %.2f", s)
	}
	// A pill: its top side is a quarter of its outline (60 px of sides, 2 ends of radius 19).
	wide := solid(100, 40, tH)
	fill(wide, Rect{20, 0, 60, 2}, tAccent)
	if s := RingShare(wide, Rect{0, 0, 100, 40}, 1, tAccent, tRingAccentTol); s < 0.22 || s > 0.28 {
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
		got, err := CornerRadius(img, 10, 10, 30)
		if err != nil || math.Abs(got-r) > tRadiusTol {
			t.Errorf("radius %v fits %.2f (%v)", r, got, err)
		}
	}
	if _, err := CornerRadius(solid(80, 80, tG), 10, 10, 30); err == nil {
		t.Error("a window the colour of what is around it: no error")
	}
}

// A light slab is one big light area; glyphs are many small ones.
func TestLightSlab(t *testing.T) {
	img := solid(200, 100, tH)
	for i := range 10 {
		fill(img, Rect{10 + 15*i, 10, 8, 12}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	}
	if n := LightSlab(img, Rect{0, 0, 200, 100}, tSlabLightness); n != 96 {
		t.Errorf("glyphs: largest %d", n)
	}
	fill(img, Rect{20, 40, 30, 30}, color.NRGBA{0xF4, 0xF6, 0xF8, 255})
	if n := LightSlab(img, Rect{0, 0, 200, 100}, tSlabLightness); n != 900 {
		t.Errorf("a slab: largest %d", n)
	}
}

// Stem over cap height: vertical strokes measure it, a crossbar does not.
func TestStrokeRatio(t *testing.T) {
	for _, stem := range []int{8, 14} {
		img := solid(200, 120, tBlack)
		fg := color.NRGBA{0xE6, 0xED, 0xF2, 255}
		// An H and an I, 70 px tall.
		fill(img, Rect{20, 20, stem, 70}, fg)
		fill(img, Rect{60, 20, stem, 70}, fg)
		fill(img, Rect{20, 50, 40 + stem, stem}, fg)
		fill(img, Rect{100, 20, stem, 70}, fg)
		got, err := StrokeRatio(img, Rect{0, 0, 200, 120}, fg, tBlack)
		if want := float64(stem) / 70; err != nil || math.Abs(got-want) > 0.001 {
			t.Errorf("stem %d: %.3f, want %.3f (%v)", stem, got, want, err)
		}
	}
	if _, err := StrokeRatio(solid(50, 50, tBlack), Rect{0, 0, 50, 50}, tAccent, tBlack); err == nil {
		t.Error("no ink: no error")
	}
}

// A pill's end over the light desk: its apex pixel is the ring antialiased against the desk, a
// blend the plain tolerance refuses; RingHit takes it, and not the desk alone.
func TestRingHitBlend(t *testing.T) {
	desk := color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	img := solid(20, 10, desk)
	fill(img, Rect{6, 0, 14, 10}, tG)
	img.SetNRGBA(6, 5, Tint(tRing, desk, 0.975))
	if _, ok := RingHit(img, 6, 5, -1, 0, tReach, tRing); !ok {
		t.Errorf("the apex %s was not taken for the ring", Hex(At(img, 6, 5)))
	}
	img.SetNRGBA(6, 5, Tint(tRing, desk, 0.5))
	if _, ok := RingHit(img, 6, 5, -1, 0, tReach, tRing); ok {
		t.Error("half a ring was taken for one")
	}
	if a, res := CoverOf(Tint(tAccent, tH, 0.18), tAccent, tH); math.Abs(a-0.18) > 0.01 || res > 1 {
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
	if n := FlatCount(img, Rect{0, 0, 40, 40}, stock, tSurfaceTol); n != 0 {
		t.Errorf("antialiasing counted: %d", n)
	}
	fill(img, Rect{10, 20, 10, 10}, stock)
	if n := FlatCount(img, Rect{0, 0, 40, 40}, stock, tSurfaceTol); n != 64 {
		t.Errorf("a 10x10 surface has 64 inner pixels, counted %d", n)
	}
}

// Marks are not areas: text-thin strokes, a 2 px ring and a 24 px disc stay marks; a solid button
// and a 6 px bar are areas larger than a mark.
func TestSolidAreas(t *testing.T) {
	img := solid(400, 200, tH)
	acc := []color.NRGBA{tAccent}
	all := Rect{0, 0, 400, 200}
	fill(img, Rect{10, 10, 200, 3}, tAccent) // a slider's fill
	fill(img, Rect{10, 30, 4, 60}, tAccent)  // a 4 px stroke
	fill(img, Rect{50, 50, 24, 24}, tAccent) // the disc's box
	fill(img, Rect{100, 40, 40, 2}, tAccent) // a ring's side
	if a := SolidAreas(img, all, acc, tSweepTol, tMarkStroke*1, tMarkDisc*1); len(a) != 0 {
		t.Errorf("marks taken for areas: %v", a)
	}
	fill(img, Rect{200, 100, 120, 36}, tAccent)
	fill(img, Rect{20, 150, 300, 6}, tAccent)
	a := SolidAreas(img, all, acc, tSweepTol, tMarkStroke*1, tMarkDisc*1)
	if len(a) != 2 || a[0] != (Rect{20, 150, 300, 6}) && a[1] != (Rect{20, 150, 300, 6}) {
		t.Errorf("a button and a bar: %v", a)
	}
	if s := Share(img, all, acc, tSweepTol); s < 0.05 || s > 0.1 {
		t.Errorf("share %.3f", s)
	}
	// Tela's teal in the dock is not light's accent.
	if s := Share(solid(10, 10, color.NRGBA{0x00, 0x82, 0x92, 255}), Rect{0, 0, 10, 10}, []color.NRGBA{{0x00, 0x7A, 0x85, 255}}, tSweepTol); s != 0 {
		t.Errorf("Tela's teal counted: %.2f", s)
	}
}

// A line across a pane is found where it is, past text on the rows above it; a column of icons
// is no separator, the line between two panes is.
func TestRules(t *testing.T) {
	shelf, c := color.NRGBA{0xE9, 0xED, 0xF0, 255}, color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	line := color.NRGBA{0xCF, 0xD5, 0xD9, 255}
	img := solid(300, 200, c)
	fill(img, Rect{0, 0, 100, 200}, shelf)
	fill(img, Rect{100, 0, 1, 200}, line)
	fill(img, Rect{0, 47, 300, 1}, line)
	fill(img, Rect{140, 20, 60, 10}, tInk) // a title
	for y := 60; y < 200; y += 20 {
		fill(img, Rect{20, y, 16, 16}, tInk) // the sidebar's icons
	}
	if y, col, ok := HRule(img, 110, 290, 8, 80, tSurfaceTol); !ok || y != 47 || col != line {
		t.Errorf("content's rule: %d %s %v", y, Hex(col), ok)
	}
	if y, _, ok := HRule(img, 10, 90, 8, 80, tSurfaceTol); !ok || y != 47 {
		t.Errorf("sidebar's rule: %d %v", y, ok)
	}
	if x, _, ok := VRule(img, 10, 150, 60, 190, 1, tSurfaceTol); !ok || x != 100 {
		t.Errorf("separator: %d %v", x, ok)
	}
	if _, _, ok := HRule(solid(100, 100, c), 0, 100, 8, 80, tSurfaceTol); ok {
		t.Error("a rule found on a bare pane")
	}
}

// At 2x a mark is twice the pixels and still a mark; an area is an area at any scale.
func TestSolidAreasScaled(t *testing.T) {
	img := solid(800, 400, tH)
	acc := []color.NRGBA{tAccent}
	all := Rect{0, 0, 800, 400}
	fill(img, Rect{20, 60, 8, 120}, tAccent)   // a 4 px stroke, at 2x
	fill(img, Rect{100, 100, 48, 48}, tAccent) // the 24 px disc's box, at 2x
	if a := SolidAreas(img, all, acc, tSweepTol, tMarkStroke*2, tMarkDisc*2); len(a) != 0 {
		t.Errorf("marks at 2x taken for areas: %v", a)
	}
	if a := SolidAreas(img, all, acc, tSweepTol, tMarkStroke*1, tMarkDisc*1); len(a) != 2 {
		t.Errorf("the same pixels at 1x are areas: %v", a)
	}
	fill(img, Rect{400, 200, 240, 72}, tAccent)
	if a := SolidAreas(img, all, acc, tSweepTol, tMarkStroke*2, tMarkDisc*2); len(a) != 1 {
		t.Errorf("a button at 2x: %v", a)
	}
}

// A separator a stage pixel wide is two physical ones at 2x: found with its width, missed without.
func TestVRuleScaled(t *testing.T) {
	shelf, c := color.NRGBA{0xE9, 0xED, 0xF0, 255}, color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	line := color.NRGBA{0xCF, 0xD5, 0xD9, 255}
	img := solid(600, 400, c)
	fill(img, Rect{0, 0, 200, 400}, shelf)
	fill(img, Rect{200, 0, 2, 400}, line)
	if x, col, ok := VRule(img, 20, 300, 120, 380, 2, tSurfaceTol); !ok || x != 200 || col != line {
		t.Errorf("2x separator: %d %v", x, ok)
	}
	if _, _, ok := VRule(img, 20, 300, 120, 380, 1, tSurfaceTol); ok {
		t.Error("a 2 px line read as 1 px wide")
	}
}
