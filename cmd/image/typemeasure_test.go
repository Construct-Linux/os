package main

import (
	"image"
	"image/color"
	"math"
	"testing"
)

var (
	tTypeFG = color.NRGBA{0xE6, 0xED, 0xF2, 255}
	tTypeBG = color.NRGBA{0x17, 0x20, 0x28, 255}
)

// ink draws the rectangle [x0,x1) x [y0,y1), in fractional pixels, with each pixel covered by
// exactly the area inside it: a renderer's antialiasing without its rounding.
func ink(img *image.NRGBA, x0, y0, x1, y1 float64) {
	for y := int(math.Floor(y0)); y < int(math.Ceil(y1)); y++ {
		for x := int(math.Floor(x0)); x < int(math.Ceil(x1)); x++ {
			a := (math.Min(x1, float64(x+1)) - math.Max(x0, float64(x))) * (math.Min(y1, float64(y+1)) - math.Max(y0, float64(y)))
			blendAt(img, x, y, tTypeFG, a)
		}
	}
}

func inkOf(img *image.NRGBA) inkMap { return coverageOf(img, img.Bounds(), tTypeFG, tTypeBG) }

func near64(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// A stem on whole pixels has hard edges (0.8 px by sample centres), reaches the text's colour and
// is as wide as drawn; half a pixel off, its edges spread over another pixel and it is as wide.
func TestMeasureStems(t *testing.T) {
	hard := solid(40, 60, tTypeBG)
	ink(hard, 10, 10, 13, 50)
	s := measureStems(inkOf(hard), map[int]bool{0: true})
	if !near64(s.Edge, 0.8, 1e-6) || !near64(s.Peak, 1, 0.01) || !near64(s.StemMean, 3, 0.02) || s.StemVar > 1e-6 || s.Stems != 1 {
		t.Errorf("hard: %+v", s)
	}
	soft := solid(40, 60, tTypeBG)
	ink(soft, 10.5, 10, 13.5, 50)
	s = measureStems(inkOf(soft), nil)
	if !near64(s.Edge, 1.6, 0.02) || !near64(s.StemMean, 3, 0.02) {
		t.Errorf("half a pixel off: %+v", s)
	}
	// A stem thinner than a pixel never reaches the text's colour.
	thin := solid(40, 60, tTypeBG)
	ink(thin, 10.25, 10, 11, 50)
	if s := measureStems(inkOf(thin), map[int]bool{0: true}); !near64(s.Peak, 0.75, 0.02) {
		t.Errorf("thin: %+v", s)
	}
	// Two glyphs, 3 and 4 px: their widths' variance; only the one-stem glyph's peak counts.
	two := solid(60, 60, tTypeBG)
	ink(two, 10, 10, 13, 50)
	ink(two, 30.5, 10, 31, 50)
	ink(two, 40, 10, 44, 50)
	s = measureStems(inkOf(two), map[int]bool{1: true})
	if s.Stems != 3 || !near64(s.Peak, 0.5, 0.02) {
		t.Errorf("peak of glyph 1 only: %+v", s)
	}
	// An 'H': its bar, in the middle of its height, is not a stem.
	h := solid(40, 60, tTypeBG)
	ink(h, 10, 10, 13, 50)
	ink(h, 20, 10, 23, 50)
	ink(h, 10, 28, 23, 32)
	if s := measureStems(inkOf(h), nil); s.Stems != 1 || !near64(s.StemMean, 3, 0.02) {
		t.Errorf("H: %+v", s)
	}
	if s := measureStems(inkOf(solid(20, 20, tTypeBG)), nil); s.Stems != 0 || s.Edge != 0 {
		t.Errorf("no ink: %+v", s)
	}
}

// Glyphs on one baseline and one x-height do not stray; one half a pixel low does; a line drawn
// half a pixel off the grid has blurred horizontals and its baseline through the middle of a row.
func TestMeasureAlign(t *testing.T) {
	line := func(ys ...float64) *image.NRGBA {
		img := solid(80, 60, tTypeBG)
		for i, y := range ys {
			ink(img, float64(10+i*15), y, float64(18+i*15), y+20)
		}
		return img
	}
	a := measureAlign(inkOf(line(20, 20, 20, 20)))
	if a.Glyphs != 4 || a.BaseJitter > 1e-6 || a.XJitter > 1e-6 || !near64(a.HEdge, 0.8, 1e-6) || a.Phase > 1e-6 {
		t.Errorf("aligned: %+v", a)
	}
	a = measureAlign(inkOf(line(20, 20, 20.5, 20)))
	if !near64(a.BaseJitter, math.Sqrt(3)/8, 0.01) || !near64(a.XJitter, math.Sqrt(3)/8, 0.01) {
		t.Errorf("one half a pixel low: %+v", a)
	}
	a = measureAlign(inkOf(line(20.5, 20.5, 20.5, 20.5)))
	if a.BaseJitter > 1e-6 || !near64(a.HEdge, 1.6, 0.02) || !near64(a.Phase, 0.5, 0.01) {
		t.Errorf("off the grid: %+v", a)
	}
}

func TestEdgeWidthAndRuns(t *testing.T) {
	p := []float64{0, 0.25, 0.5, 0.75, 1, 1}
	if w, ok := edgeWidth(p, 0, 1, 1); !ok || !near64(w, 3.2, 1e-9) {
		t.Errorf("ramp %v %v", w, ok)
	}
	if _, ok := edgeWidth([]float64{0, 0, 0}, 0, 1, 1); ok {
		t.Error("an edge in no ink")
	}
	if r := runs([]float64{0, 0.5, 1, 0, 0, 0.3}); len(r) != 2 || r[0] != [2]int{1, 2} || r[1] != [2]int{5, 5} {
		t.Errorf("runs %v", r)
	}
	if m, v := meanVar([]float64{1, 3}); m != 2 || v != 1 {
		t.Errorf("meanVar %v %v", m, v)
	}
	if median(nil) != 0 || median([]float64{3, 1, 2}) != 2 {
		t.Error("median")
	}
}

// Light text on dark and dark on light read alike: coverage is of the text's colour over its
// ground, whichever is lighter.
func TestCoverageOfLight(t *testing.T) {
	fg, bg := color.NRGBA{0x11, 0x18, 0x1E, 255}, color.NRGBA{0xF4, 0xF6, 0xF8, 255}
	img := solid(10, 10, bg)
	blendAt(img, 3, 3, fg, 0.5)
	img.SetNRGBA(4, 3, fg)
	c := coverageOf(img, img.Bounds(), fg, bg)
	if !near64(c.at(3, 3), 0.5, 0.01) || c.at(4, 3) != 1 || c.at(0, 0) != 0 {
		t.Errorf("%v %v %v", c.at(3, 3), c.at(4, 3), c.at(0, 0))
	}
}
