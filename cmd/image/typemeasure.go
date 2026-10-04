package main

import (
	"image"
	"image/color"
	"math"
	"sort"
)

// What makes text look sharp, measured on a screenshot's crop of it: how wide its edges are, how
// close a thin stem gets to the text's colour, how even its stems are, and whether its glyphs sit
// on one baseline and one x-height. Pixels are read as coverage: how much of the text's colour
// over its ground each one is, 0 to 1 (coverOf).

// inkMin is the coverage where ink starts: under it, a pixel is the ground and its rounding.
const inkMin = 0.08

// inkMap is a crop's pixels as coverage of fg over bg.
type inkMap struct {
	w, h int
	a    []float64
}

func coverageOf(img image.Image, r image.Rectangle, fg, bg color.NRGBA) inkMap {
	r = r.Intersect(img.Bounds())
	c := inkMap{w: r.Dx(), h: r.Dy(), a: make([]float64, r.Dx()*r.Dy())}
	for y := range c.h {
		for x := range c.w {
			a, _ := coverOf(at(img, r.Min.X+x, r.Min.Y+y), fg, bg)
			c.a[y*c.w+x] = a
		}
	}
	return c
}

func (c inkMap) at(x, y int) float64 { return c.a[y*c.w+x] }

// span is a glyph's columns, x0 to x1 inclusive, and the rows its ink covers.
type span struct{ x0, x1, y0, y1 int }

// glyphs are the crop's runs of columns with ink, left to right, each with the rows of its ink:
// spaced glyphs, one each.
func (c inkMap) glyphs() []span {
	var out []span
	in := false
	var s span
	for x := 0; x <= c.w; x++ {
		ink := false
		if x < c.w {
			for y := range c.h {
				if c.at(x, y) >= inkMin {
					ink = true
					break
				}
			}
		}
		switch {
		case ink && !in:
			s, in = span{x0: x}, true
		case !ink && in:
			s.x1, in = x-1, false
			s.y0, s.y1 = -1, -1
			for y := range c.h {
				for xx := s.x0; xx <= s.x1; xx++ {
					if c.at(xx, y) >= inkMin {
						if s.y0 < 0 {
							s.y0 = y
						}
						s.y1 = y
						break
					}
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// crossing is where a profile p, from i0 stepping by step, first reaches level, between samples
// by linear interpolation: sample positions are pixel centres.
func crossing(p []float64, i0, step int, level float64) (float64, bool) {
	prev := i0
	for i := i0; i >= 0 && i < len(p); i += step {
		if p[i] >= level {
			if i == i0 {
				return float64(i), true
			}
			d := p[i] - p[prev]
			f := 0.0
			if d > 0 {
				f = (level - p[prev]) / d
			}
			return float64(prev) + f*float64(step), true
		}
		prev = i
	}
	return 0, false
}

// edgeWidth is a profile's 10-90% rise from i0 in the direction step, relative to its peak: a
// hard step from ground to ink measures 0.8 px (sample centres a pixel apart), a ramp over one
// more pixel 1.6.
func edgeWidth(p []float64, i0, step int, peak float64) (float64, bool) {
	lo, ok1 := crossing(p, i0, step, 0.1*peak)
	hi, ok2 := crossing(p, i0, step, 0.9*peak)
	if !ok1 || !ok2 {
		return 0, false
	}
	return math.Abs(hi - lo), true
}

// stemStats are a glyph line's vertical stems, measured in the bands of every glyph above and
// below its middle (an 'H”s bar is in the middle): each row's run of ink is a stem's cross
// section. ones are the glyphs, by index, that are a single stem ('l', 'I'); their peak is the
// stroke's. Without ones, every stem's peak counts: text whose glyphs nobody chose.
type stemStats struct {
	// Edge is the mean 10-90% width of the stems' edges, in px.
	Edge float64 `json:"edge"`
	// Peak is the median coverage a single-stem glyph's stem reaches: 1 is the text's colour.
	Peak float64 `json:"peak"`
	// StemMean and StemVar are the stems' widths, as the sum of their coverage across, per glyph.
	StemMean float64 `json:"stem_mean"`
	StemVar  float64 `json:"stem_var"`
	Stems    int     `json:"stems"`
}

func measureStems(c inkMap, ones map[int]bool) stemStats {
	var edges, peaks, widths []float64
	for gi, g := range c.glyphs() {
		hgt := g.y1 - g.y0 + 1
		if hgt < 4 {
			continue
		}
		var gw []float64
		for y := g.y0; y <= g.y1; y++ {
			f := float64(y-g.y0) / float64(hgt)
			if (f < 0.15 || f > 0.38) && (f < 0.62 || f > 0.85) {
				continue
			}
			// The row's profile across the glyph, a pixel of ground either side.
			x0, x1 := max(g.x0-1, 0), min(g.x1+1, c.w-1)
			p := make([]float64, x1-x0+1)
			for x := x0; x <= x1; x++ {
				p[x-x0] = c.at(x, y)
			}
			for _, run := range runs(p) {
				peak, width := 0.0, 0.0
				for i := run[0]; i <= run[1]; i++ {
					peak = max(peak, p[i])
					width += p[i]
				}
				if l, ok := edgeWidth(p, max(run[0]-1, 0), 1, peak); ok {
					edges = append(edges, l)
				}
				if r, ok := edgeWidth(p, min(run[1]+1, len(p)-1), -1, peak); ok {
					edges = append(edges, r)
				}
				gw = append(gw, width)
				if ones == nil || ones[gi] {
					peaks = append(peaks, peak)
				}
			}
		}
		if len(gw) > 0 {
			widths = append(widths, mean(gw))
		}
	}
	m, v := meanVar(widths)
	return stemStats{Edge: mean(edges), Peak: median(peaks), StemMean: m, StemVar: v, Stems: len(widths)}
}

// runs are p's runs of ink, first and last index each.
func runs(p []float64) [][2]int {
	var out [][2]int
	start := -1
	for i := 0; i <= len(p); i++ {
		ink := i < len(p) && p[i] >= inkMin
		switch {
		case ink && start < 0:
			start = i
		case !ink && start >= 0:
			out = append(out, [2]int{start, i - 1})
			start = -1
		}
	}
	return out
}

// alignStats are a line of flat-topped, flat-bottomed glyphs ('x', 'z'): where each one's top and
// bottom edge is, to a fraction of a pixel, and how sharp those horizontal edges are.
type alignStats struct {
	// BaseJitter and XJitter are the standard deviations of the glyphs' bottom and top edges, in px.
	BaseJitter float64 `json:"base_jitter"`
	XJitter    float64 `json:"x_jitter"`
	// HEdge is the mean 10-90% width of the top and bottom edges: blur across a horizontal.
	HEdge float64 `json:"h_edge"`
	// Phase is the mean fractional position of the bottom edge in its pixel: 0 is an edge on a
	// pixel boundary, 0.5 one through the middle of a row.
	Phase  float64 `json:"phase"`
	Glyphs int     `json:"glyphs"`
}

func measureAlign(c inkMap) alignStats {
	var tops, bottoms, hedges, phases []float64
	for _, g := range c.glyphs() {
		// The glyph's vertical profile: the most of it any of its columns covers in a row.
		p := make([]float64, c.h)
		peak := 0.0
		for y := range c.h {
			for x := g.x0; x <= g.x1; x++ {
				p[y] = max(p[y], c.at(x, y))
			}
			peak = max(peak, p[y])
		}
		if peak < inkMin {
			continue
		}
		top, ok1 := crossing(p, 0, 1, 0.5*peak)
		bot, ok2 := crossing(p, c.h-1, -1, 0.5*peak)
		if !ok1 || !ok2 {
			continue
		}
		// Row i's sample is at i+0.5 on the pixel grid, where an edge's position is read.
		tops = append(tops, top+0.5)
		bottoms = append(bottoms, bot+0.5)
		phases = append(phases, (bot+0.5)-math.Floor(bot+0.5))
		if e, ok := edgeWidth(p, 0, 1, peak); ok {
			hedges = append(hedges, e)
		}
		if e, ok := edgeWidth(p, c.h-1, -1, peak); ok {
			hedges = append(hedges, e)
		}
	}
	_, tv := meanVar(tops)
	_, bv := meanVar(bottoms)
	return alignStats{BaseJitter: math.Sqrt(bv), XJitter: math.Sqrt(tv), HEdge: mean(hedges), Phase: mean(phases), Glyphs: len(tops)}
}

func mean(v []float64) float64 {
	m, _ := meanVar(v)
	return m
}

func meanVar(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	m := 0.0
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	s := 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return m, s / float64(len(v))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}
