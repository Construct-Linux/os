package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// lfRect is an actor's or a window's extents on the screen, in its pixels.
type lfRect struct{ X, Y, W, H int }

func (r lfRect) inset(dx, dy int) lfRect { return lfRect{r.X + dx, r.Y + dy, r.W - 2*dx, r.H - 2*dy} }

func (r lfRect) image() image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }

func (r lfRect) String() string { return fmt.Sprintf("%dx%d+%d+%d", r.W, r.H, r.X, r.Y) }

func at(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func near(a, b color.NRGBA, tol int) bool { return channelDelta(a, b) <= tol }

// modeColor is r's most common colour and the share of r it covers: a surface's own colour, which
// its text and icons do not outnumber.
func modeColor(img image.Image, r lfRect) (color.NRGBA, float64) {
	b := r.image().Intersect(img.Bounds())
	if b.Empty() {
		return color.NRGBA{}, 0
	}
	n := map[color.NRGBA]int{}
	var best color.NRGBA
	bestN := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := at(img, x, y)
			n[c]++
			if n[c] > bestN {
				best, bestN = c, n[c]
			}
		}
	}
	return best, float64(bestN) / float64(b.Dx()*b.Dy())
}

// countNear is how many of r's pixels are within tol of want.
func countNear(img image.Image, r lfRect, want color.NRGBA, tol int) int {
	b := r.image().Intersect(img.Bounds())
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if near(at(img, x, y), want, tol) {
				n++
			}
		}
	}
	return n
}

// coverOf reads c as fg drawn over bg: the alpha that blend would have (0 to 1), and how far c
// is from it, in a channel. An antialiased ring's pixel is the ring over what is outside it.
func coverOf(c, fg, bg color.NRGBA) (float64, int) {
	dr, dg, db := float64(fg.R)-float64(bg.R), float64(fg.G)-float64(bg.G), float64(fg.B)-float64(bg.B)
	den := dr*dr + dg*dg + db*db
	if den == 0 {
		return 1, channelDelta(c, fg)
	}
	a := ((float64(c.R)-float64(bg.R))*dr + (float64(c.G)-float64(bg.G))*dg + (float64(c.B)-float64(bg.B))*db) / den
	a = min(max(a, 0), 1)
	return a, channelDelta(c, tint(fg, bg, a))
}

// ringHit looks for a 1 px ring at (x, y) and up to reach px either way along the normal (nx, ny):
// a ring lands a pixel to either side of an extent rounded from a fractional allocation. A pixel is
// the ring when it is within lfRingTol of it, or the ring antialiased over the pixel just outside it,
// covering at least lfRingCover of it.
func ringHit(img image.Image, x, y, nx, ny, reach int, ring color.NRGBA) (image.Point, bool) {
	for k := 0; k <= reach; k++ {
		for _, s := range []int{k, -k} {
			p := image.Pt(x+s*nx, y+s*ny)
			o := image.Pt(p.X+nx, p.Y+ny)
			if !p.In(img.Bounds()) {
				continue
			}
			c := at(img, p.X, p.Y)
			if near(c, ring, lfRingTol) {
				return p, true
			}
			if o.In(img.Bounds()) {
				if a, res := coverOf(c, ring, at(img, o.X, o.Y)); a >= lfRingCover && res <= lfRingTol {
					return p, true
				}
			}
			if k == 0 {
				break
			}
		}
	}
	return image.Point{}, false
}

// flatCount is how many of r's pixels are within tol of want with their four neighbours too: a
// surface of that colour, not the antialiasing of an edge that passes through it.
func flatCount(img image.Image, r lfRect, want color.NRGBA, tol int) int {
	b := r.image().Intersect(img.Bounds())
	n := 0
	for y := b.Min.Y + 1; y < b.Max.Y-1; y++ {
		for x := b.Min.X + 1; x < b.Max.X-1; x++ {
			if near(at(img, x, y), want, tol) && near(at(img, x-1, y), want, tol) && near(at(img, x+1, y), want, tol) &&
				near(at(img, x, y-1), want, tol) && near(at(img, x, y+1), want, tol) {
				n++
			}
		}
	}
	return n
}

// accentMask is which of r's pixels are within tol of any of cols, row by row.
func accentMask(img image.Image, r lfRect, cols []color.NRGBA, tol int) ([]bool, image.Rectangle) {
	b := r.image().Intersect(img.Bounds())
	m := make([]bool, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := at(img, x, y)
			for _, w := range cols {
				if near(c, w, tol) {
					m[(y-b.Min.Y)*b.Dx()+x-b.Min.X] = true
					break
				}
			}
		}
	}
	return m, b
}

// accentShare is the share of r's pixels within tol of any of cols.
func accentShare(img image.Image, r lfRect, cols []color.NRGBA, tol int) float64 {
	m, b := accentMask(img, r, cols, tol)
	if len(m) == 0 {
		return 0
	}
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return float64(n) / float64(b.Dx()*b.Dy())
}

// solidAreas are r's areas of cols larger than a mark: what is left of them once every stroke up
// to lfMarkStroke px is eroded away (a square of side lfMarkStroke+1 must fit), grown back, and
// wider or taller than lfMarkDisc - both scale physical pixels a stage pixel. Text, rings, a
// slider's fill and the quick toggle's disc are marks; a solid button or a filled toggle is an
// area.
func solidAreas(img image.Image, r lfRect, cols []color.NRGBA, scale int) []lfRect {
	m, b := accentMask(img, r, cols, lfSweepTol)
	w, h := b.Dx(), b.Dy()
	k := lfMarkStroke*scale/2 + 1
	// Erosion by a square, as a horizontal run and then a vertical one.
	erode := func(src []bool, dx, dy int) []bool {
		out := make([]bool, len(src))
		for y := range h {
			for x := range w {
				ok := true
				for s := -k + 1; s < k && ok; s++ {
					xx, yy := x+s*dx, y+s*dy
					ok = xx >= 0 && yy >= 0 && xx < w && yy < h && src[yy*w+xx]
				}
				out[y*w+x] = ok
			}
		}
		return out
	}
	core := erode(erode(m, 1, 0), 0, 1)
	seen := make([]bool, len(core))
	var out []lfRect
	var stack []int
	for i, v := range core {
		if !v || seen[i] {
			continue
		}
		box := image.Rect(i%w, i/w, i%w+1, i/w+1)
		seen[i] = true
		stack = append(stack[:0], i)
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := j%w, j/w
			box = box.Union(image.Rect(x, y, x+1, y+1))
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h || seen[yy*w+xx] || !core[yy*w+xx] {
						continue
					}
					seen[yy*w+xx] = true
					stack = append(stack, yy*w+xx)
				}
			}
		}
		// Grown back by what erosion took from each side.
		box = box.Inset(-(k - 1))
		if box.Dx() > lfMarkDisc*scale || box.Dy() > lfMarkDisc*scale {
			out = append(out, lfRect{b.Min.X + box.Min.X, b.Min.Y + box.Min.Y, box.Dx(), box.Dy()})
		}
	}
	return out
}

// hRule finds a horizontal 1 px line in the columns x0 to x1, from y0 down to y1: the first row
// whose most common colour covers at least lfRuleShare of it and differs, beyond lfSurfaceTol, from
// the most common colour of the row above. A line of text never covers a row; a gradient's step
// is within the tolerance.
func hRule(img image.Image, x0, x1, y0, y1 int) (int, color.NRGBA, bool) {
	prev, _ := modeColor(img, lfRect{x0, y0 - 1, x1 - x0, 1})
	for y := y0; y < y1; y++ {
		c, share := modeColor(img, lfRect{x0, y, x1 - x0, 1})
		if share >= lfRuleShare && channelDelta(c, prev) > lfSurfaceTol {
			return y, c, true
		}
		prev = c
	}
	return 0, color.NRGBA{}, false
}

// vRule is hRule across: the first column from x0 to x1, in the rows y0 to y1, that is a line
// width px wide (a stage pixel's physical ones) different from the columns on both sides of it.
func vRule(img image.Image, x0, x1, y0, y1, width int) (int, color.NRGBA, bool) {
	col := func(x int) (color.NRGBA, float64) { return modeColor(img, lfRect{x, y0, 1, y1 - y0}) }
	for x := x0; x < x1; x++ {
		c, share := col(x)
		l, _ := col(x - 1)
		r, _ := col(x + width)
		if share >= lfRuleShare && channelDelta(c, l) > lfSurfaceTol && channelDelta(c, r) > lfSurfaceTol {
			return x, c, true
		}
	}
	return 0, color.NRGBA{}, false
}

// luminance is WCAG 2.2's relative luminance.
func luminance(c color.NRGBA) float64 {
	ch := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// contrast is WCAG 2.2's contrast ratio, 1 to 21.
func contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// over is fg, at its alpha, over an opaque bg.
func over(fg, bg color.NRGBA) color.NRGBA {
	return tint(color.NRGBA{fg.R, fg.G, fg.B, 255}, bg, float64(fg.A)/255)
}

// tint is c at alpha t over an opaque surface: how the shell draws its state layers and tints.
func tint(c, surface color.NRGBA, t float64) color.NRGBA {
	m := func(a, b uint8) uint8 { return uint8(math.Round(float64(a)*t + float64(b)*(1-t))) }
	return color.NRGBA{m(c.R, surface.R), m(c.G, surface.G), m(c.B, surface.B), 255}
}

// ringShare is the share of points on a pill's outline, inset px inside its extents, within
// lfRingAccentTol of want: a 2 px ring drawn inside the edge is sampled down its middle with inset 1. A circle is a
// pill as long as it is wide.
func ringShare(img image.Image, r lfRect, inset float64, want color.NRGBA) float64 {
	d := float64(min(r.W, r.H))
	rad := d/2 - inset
	x0, y0 := float64(r.X), float64(r.Y)
	wide := r.W >= r.H
	c1 := [2]float64{x0 + d/2, y0 + d/2}
	c2 := c1
	if wide {
		c1[1], c2[1] = y0+float64(r.H)/2, y0+float64(r.H)/2
		c2[0] = x0 + float64(r.W) - d/2
	} else {
		c1[0], c2[0] = x0+float64(r.W)/2, x0+float64(r.W)/2
		c2[1] = y0 + float64(r.H) - d/2
	}
	hits, n := 0, 0
	sample := func(x, y float64) {
		p := image.Pt(int(math.Floor(x)), int(math.Floor(y)))
		if !p.In(img.Bounds()) {
			return
		}
		n++
		if near(at(img, p.X, p.Y), want, lfRingAccentTol) {
			hits++
		}
	}
	// One sample a pixel of outline, on the ends as on the sides.
	steps := max(16, int(math.Ceil(2*math.Pi*rad)))
	for i := range steps {
		a := float64(i) * 2 * math.Pi / float64(steps)
		dx, dy := math.Cos(a), math.Sin(a)
		// Each half of the outline is its own end's: the right end for a wide pill, the bottom
		// for a tall one.
		c := c1
		if (wide && dx > 0) || (!wide && dy > 0) {
			c = c2
		}
		sample(c[0]+rad*dx, c[1]+rad*dy)
	}
	if wide {
		for x := c1[0]; x <= c2[0]; x++ {
			sample(x, c1[1]-rad)
			sample(x, c1[1]+rad)
		}
	} else {
		for y := c1[1]; y <= c2[1]; y++ {
			sample(c1[0]-rad, y)
			sample(c1[0]+rad, y)
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hits) / float64(n)
}

// cornerRadius fits the radius of a window's rounded top-left corner, whose frame starts at (x, y):
// row by row, the first pixel at least half the window's own colour (sampled past the curve) against
// what is outside it (the frame's corner pixel, which a rounded corner leaves outside), and the
// circle that best gives those columns. Rows that fit no circle (a ring drawn inside the frame) are
// capped, so one does not move the fit.
func cornerRadius(img image.Image, x, y, maxR int) (float64, error) {
	inside, _ := modeColor(img, lfRect{x + maxR + 2, y + 1, 32, 4})
	outside := at(img, x, y)
	if channelDelta(inside, outside) < lfCornerContrast {
		return 0, fmt.Errorf("the window (%s) and what is outside its corner (%s) are too alike to see the corner", hexOf(inside), hexOf(outside))
	}
	edge := make([]int, maxR)
	for dy := range maxR {
		edge[dy] = maxR
		for dx := range maxR {
			if a, _ := coverOf(at(img, x+dx, y+dy), inside, outside); a >= 0.5 {
				edge[dy] = dx
				break
			}
		}
	}
	best, bestErr := 0.0, math.Inf(1)
	for r := 0.0; r <= float64(maxR); r += 0.25 {
		e := 0.0
		for dy := range maxR {
			d := float64(edge[dy] - predictedEdge(r, dy))
			e += min(d*d, 9)
		}
		if e < bestErr {
			best, bestErr = r, e
		}
	}
	return best, nil
}

// predictedEdge is the first column, from a corner of radius r, whose pixel centre is inside the
// curve in row dy.
func predictedEdge(r float64, dy int) int {
	cy := float64(dy) + 0.5
	if cy >= r {
		return 0
	}
	dx := r - math.Sqrt(r*r-(r-cy)*(r-cy))
	return max(0, int(math.Ceil(dx-0.5)))
}

// lightness is HSL's, 0 to 1.
func lightness(c color.NRGBA) float64 {
	return (float64(max(c.R, c.G, c.B)) + float64(min(c.R, c.G, c.B))) / 2 / 255
}

// lightSlab is the size of the largest 4-connected area in r lighter than l: glyphs and icons are
// small areas, a light slab is one big one.
func lightSlab(img image.Image, r lfRect, l float64) int {
	b := r.image().Intersect(img.Bounds())
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	light := make([]bool, w*h)
	for y := range h {
		for x := range w {
			light[y*w+x] = lightness(at(img, b.Min.X+x, b.Min.Y+y)) > l
		}
	}
	seen := make([]bool, w*h)
	largest := 0
	var stack []int
	for i := range light {
		if !light[i] || seen[i] {
			continue
		}
		size := 0
		seen[i] = true
		stack = append(stack[:0], i)
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			size++
			x, y := j%w, j/w
			for _, n := range [][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
				if n[0] < 0 || n[1] < 0 || n[0] >= w || n[1] >= h {
					continue
				}
				k := n[1]*w + n[0]
				if light[k] && !seen[k] {
					seen[k] = true
					stack = append(stack, k)
				}
			}
		}
		largest = max(largest, size)
	}
	return largest
}

// strokeRatio is a line of text's stem width over its cap height, in r: pixels nearer fg than bg
// are ink, the cap height is the ink's height, and the stem the median horizontal run of ink no
// longer than a third of it (longer runs are bars and bowls' tops).
func strokeRatio(img image.Image, r lfRect, fg, bg color.NRGBA) (float64, error) {
	b := r.image().Intersect(img.Bounds())
	dist := func(a, c color.NRGBA) int {
		dr, dg, db := int(a.R)-int(c.R), int(a.G)-int(c.G), int(a.B)-int(c.B)
		return dr*dr + dg*dg + db*db
	}
	top, bottom := b.Max.Y, b.Min.Y-1
	var runs [][]int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		var row []int
		run := 0
		for x := b.Min.X; x <= b.Max.X; x++ {
			if x < b.Max.X {
				if c := at(img, x, y); dist(c, fg) < dist(c, bg) {
					run++
					continue
				}
			}
			if run > 0 {
				row = append(row, run)
				run = 0
			}
		}
		if len(row) > 0 {
			top, bottom = min(top, y), max(bottom, y)
		}
		runs = append(runs, row)
	}
	if bottom < top {
		return 0, errors.New("no ink: nothing nearer the text's colour than its surface's")
	}
	capH := bottom - top + 1
	var stems []int
	for _, row := range runs {
		for _, n := range row {
			if n*3 <= capH {
				stems = append(stems, n)
			}
		}
	}
	if len(stems) == 0 {
		return 0, errors.New("no stems: every run of ink is a bar")
	}
	sort.Ints(stems)
	return float64(stems[len(stems)/2]) / float64(capH), nil
}

// markBox is the extents of r's pixels brighter than markLevel in any channel: the wallpaper's mark,
// as plymouth.go measures it for MarkCenter.
func markBox(img image.Image, r lfRect) (lfRect, bool) {
	b := r.image().Intersect(img.Bounds())
	box := image.Rectangle{}
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := at(img, x, y); int(max(c.R, c.G, c.B)) > markLevel {
				p := image.Rect(x, y, x+1, y+1)
				if found {
					box = box.Union(p)
				} else {
					box, found = p, true
				}
			}
		}
	}
	return lfRect{box.Min.X, box.Min.Y, box.Dx(), box.Dy()}, found
}

// focusDiff compares two screenshots inside r, a before and an after the key focus moved: of the
// pixels that differ beyond the needles' channel tolerance, how many are within tol of accent in
// b - the ring drawn - and the share that are in a or in b: the ring leaves the control the focus
// left as it reaches the next, and both are the move.
func focusDiff(a, b image.Image, r lfRect, accent color.NRGBA, tol int) (drawn int, share float64) {
	rb := r.image().Intersect(a.Bounds()).Intersect(b.Bounds())
	changed, ring := 0, 0
	for y := rb.Min.Y; y < rb.Max.Y; y++ {
		for x := rb.Min.X; x < rb.Max.X; x++ {
			ca, cb := at(a, x, y), at(b, x, y)
			if channelDelta(ca, cb) <= needleChannelDelta {
				continue
			}
			changed++
			if near(cb, accent, tol) {
				drawn++
				ring++
			} else if near(ca, accent, tol) {
				ring++
			}
		}
	}
	if changed == 0 {
		return 0, 0
	}
	return drawn, float64(ring) / float64(changed)
}
