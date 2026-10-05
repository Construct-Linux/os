// Package pixels measures screenshots: colours, areas, rules, rings and corners in a rectangle,
// and the coverage of text's ink, for the harness's look checks to judge.
package pixels

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	// RingTol is a 1 px ring, against its token: antialiasing on a fractional allocation blends a
	// little of either neighbour into it.
	RingTol = 3
	// A 1 px ring on a curve's apex is antialiased against what is outside it: it still covers at
	// least this much of its pixel (the top bar's end over the light desk: 0.975). Less is no ring.
	ringCover = 0.85
	// A window's corner is fitted from the blend between it and what is outside it: closer than
	// this on every channel, the half-way crossing it looks for is noise.
	cornerContrast = 8
	// A line across a pane covers this much of its row (or column); text and icons never do.
	ruleShare = 0.9
)

// Rect is an actor's or a window's extents on the screen, in its pixels.
type Rect struct{ X, Y, W, H int }

// Inset is r shrunk by dx on the left and right and dy on the top and bottom.
func (r Rect) Inset(dx, dy int) Rect { return Rect{r.X + dx, r.Y + dy, r.W - 2*dx, r.H - 2*dy} }

func (r Rect) Image() image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }

func (r Rect) String() string { return fmt.Sprintf("%dx%d+%d+%d", r.W, r.H, r.X, r.Y) }

// At is img's pixel at (x, y), as 8-bit NRGBA.
func At(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// Near is whether a and b are within tol on every channel.
func Near(a, b color.NRGBA, tol int) bool { return ChannelDelta(a, b) <= tol }

// ModeColor is r's most common colour and the share of r it covers: a surface's own colour, which
// its text and icons do not outnumber.
func ModeColor(img image.Image, r Rect) (color.NRGBA, float64) {
	b := r.Image().Intersect(img.Bounds())
	if b.Empty() {
		return color.NRGBA{}, 0
	}
	n := map[color.NRGBA]int{}
	var best color.NRGBA
	bestN := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := At(img, x, y)
			n[c]++
			if n[c] > bestN {
				best, bestN = c, n[c]
			}
		}
	}
	return best, float64(bestN) / float64(b.Dx()*b.Dy())
}

// CountNear is how many of r's pixels are within tol of want.
func CountNear(img image.Image, r Rect, want color.NRGBA, tol int) int {
	b := r.Image().Intersect(img.Bounds())
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if Near(At(img, x, y), want, tol) {
				n++
			}
		}
	}
	return n
}

// CoverOf reads c as fg drawn over bg: the alpha that blend would have (0 to 1), and how far c
// is from it, in a channel. An antialiased ring's pixel is the ring over what is outside it.
func CoverOf(c, fg, bg color.NRGBA) (float64, int) {
	dr, dg, db := float64(fg.R)-float64(bg.R), float64(fg.G)-float64(bg.G), float64(fg.B)-float64(bg.B)
	den := dr*dr + dg*dg + db*db
	if den == 0 {
		return 1, ChannelDelta(c, fg)
	}
	a := ((float64(c.R)-float64(bg.R))*dr + (float64(c.G)-float64(bg.G))*dg + (float64(c.B)-float64(bg.B))*db) / den
	a = min(max(a, 0), 1)
	return a, ChannelDelta(c, Tint(fg, bg, a))
}

// RingHit looks for a 1 px ring at (x, y) and up to reach px either way along the normal (nx, ny):
// a ring lands a pixel to either side of an extent rounded from a fractional allocation. A pixel is
// the ring when it is within RingTol of it, or the ring antialiased over the pixel just outside it,
// covering at least ringCover of it.
func RingHit(img image.Image, x, y, nx, ny, reach int, ring color.NRGBA) (image.Point, bool) {
	for k := 0; k <= reach; k++ {
		for _, s := range []int{k, -k} {
			p := image.Pt(x+s*nx, y+s*ny)
			o := image.Pt(p.X+nx, p.Y+ny)
			if !p.In(img.Bounds()) {
				continue
			}
			c := At(img, p.X, p.Y)
			if Near(c, ring, RingTol) {
				return p, true
			}
			if o.In(img.Bounds()) {
				if a, res := CoverOf(c, ring, At(img, o.X, o.Y)); a >= ringCover && res <= RingTol {
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

// FlatCount is how many of r's pixels are within tol of want with their four neighbours too: a
// surface of that colour, not the antialiasing of an edge that passes through it.
func FlatCount(img image.Image, r Rect, want color.NRGBA, tol int) int {
	b := r.Image().Intersect(img.Bounds())
	n := 0
	for y := b.Min.Y + 1; y < b.Max.Y-1; y++ {
		for x := b.Min.X + 1; x < b.Max.X-1; x++ {
			if Near(At(img, x, y), want, tol) && Near(At(img, x-1, y), want, tol) && Near(At(img, x+1, y), want, tol) &&
				Near(At(img, x, y-1), want, tol) && Near(At(img, x, y+1), want, tol) {
				n++
			}
		}
	}
	return n
}

// accentMask is which of r's pixels are within tol of any of cols, row by row.
func accentMask(img image.Image, r Rect, cols []color.NRGBA, tol int) ([]bool, image.Rectangle) {
	b := r.Image().Intersect(img.Bounds())
	m := make([]bool, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := At(img, x, y)
			for _, w := range cols {
				if Near(c, w, tol) {
					m[(y-b.Min.Y)*b.Dx()+x-b.Min.X] = true
					break
				}
			}
		}
	}
	return m, b
}

// Share is the share of r's pixels within tol of any of cols.
func Share(img image.Image, r Rect, cols []color.NRGBA, tol int) float64 {
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

// SolidAreas are r's areas of cols (within tol) larger than a mark: what is left of them once
// every stroke up to stroke px is eroded away (a square of side stroke+1 must fit), grown back, and
// wider or taller than disc px. Text, rings, a slider's fill and the quick toggle's disc are
// marks; a solid button or a filled toggle is an area.
func SolidAreas(img image.Image, r Rect, cols []color.NRGBA, tol, stroke, disc int) []Rect {
	m, b := accentMask(img, r, cols, tol)
	w, h := b.Dx(), b.Dy()
	k := stroke/2 + 1
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
	var out []Rect
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
		if box.Dx() > disc || box.Dy() > disc {
			out = append(out, Rect{b.Min.X + box.Min.X, b.Min.Y + box.Min.Y, box.Dx(), box.Dy()})
		}
	}
	return out
}

// HRule finds a horizontal 1 px line in the columns x0 to x1, from y0 down to y1: the first row
// whose most common colour covers at least ruleShare of it and differs, beyond tol, from the most
// common colour of the row above. A line of text never covers a row; a gradient's step
// is within the tolerance.
func HRule(img image.Image, x0, x1, y0, y1, tol int) (int, color.NRGBA, bool) {
	prev, _ := ModeColor(img, Rect{x0, y0 - 1, x1 - x0, 1})
	for y := y0; y < y1; y++ {
		c, share := ModeColor(img, Rect{x0, y, x1 - x0, 1})
		if share >= ruleShare && ChannelDelta(c, prev) > tol {
			return y, c, true
		}
		prev = c
	}
	return 0, color.NRGBA{}, false
}

// VRule is HRule across: the first column from x0 to x1, in the rows y0 to y1, that is a line
// width px wide (a stage pixel's physical ones) different from the columns on both sides of it.
func VRule(img image.Image, x0, x1, y0, y1, width, tol int) (int, color.NRGBA, bool) {
	col := func(x int) (color.NRGBA, float64) { return ModeColor(img, Rect{x, y0, 1, y1 - y0}) }
	for x := x0; x < x1; x++ {
		c, share := col(x)
		l, _ := col(x - 1)
		r, _ := col(x + width)
		if share >= ruleShare && ChannelDelta(c, l) > tol && ChannelDelta(c, r) > tol {
			return x, c, true
		}
	}
	return 0, color.NRGBA{}, false
}

// Luminance is WCAG 2.2's relative luminance.
func Luminance(c color.NRGBA) float64 {
	ch := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// Contrast is WCAG 2.2's contrast ratio, 1 to 21.
func Contrast(a, b color.NRGBA) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Over is fg, at its alpha, over an opaque bg.
func Over(fg, bg color.NRGBA) color.NRGBA {
	return Tint(color.NRGBA{fg.R, fg.G, fg.B, 255}, bg, float64(fg.A)/255)
}

// Tint is c at alpha t over an opaque surface: how the shell draws its state layers and tints.
func Tint(c, surface color.NRGBA, t float64) color.NRGBA {
	m := func(a, b uint8) uint8 { return uint8(math.Round(float64(a)*t + float64(b)*(1-t))) }
	return color.NRGBA{m(c.R, surface.R), m(c.G, surface.G), m(c.B, surface.B), 255}
}

// RingShare is the share of points on a pill's outline, inset px inside its extents, within
// tol of want: a 2 px ring drawn inside the edge is sampled down its middle with inset 1. A circle
// is a pill as long as it is wide.
func RingShare(img image.Image, r Rect, inset float64, want color.NRGBA, tol int) float64 {
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
		if Near(At(img, p.X, p.Y), want, tol) {
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

// CornerRadius fits the radius of a window's rounded top-left corner, whose frame starts at (x, y):
// row by row, the first pixel at least half the window's own colour (sampled past the curve) against
// what is outside it (the frame's corner pixel, which a rounded corner leaves outside), and the
// circle that best gives those columns. Rows that fit no circle (a ring drawn inside the frame) are
// capped, so one does not move the fit.
func CornerRadius(img image.Image, x, y, maxR int) (float64, error) {
	inside, _ := ModeColor(img, Rect{x + maxR + 2, y + 1, 32, 4})
	outside := At(img, x, y)
	if ChannelDelta(inside, outside) < cornerContrast {
		return 0, fmt.Errorf("the window (%s) and what is outside its corner (%s) are too alike to see the corner", Hex(inside), Hex(outside))
	}
	edge := make([]int, maxR)
	for dy := range maxR {
		edge[dy] = maxR
		for dx := range maxR {
			if a, _ := CoverOf(At(img, x+dx, y+dy), inside, outside); a >= 0.5 {
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

// Lightness is HSL's, 0 to 1.
func Lightness(c color.NRGBA) float64 {
	return (float64(max(c.R, c.G, c.B)) + float64(min(c.R, c.G, c.B))) / 2 / 255
}

// LightSlab is the size of the largest 4-connected area in r lighter than l: glyphs and icons are
// small areas, a light slab is one big one.
func LightSlab(img image.Image, r Rect, l float64) int {
	b := r.Image().Intersect(img.Bounds())
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	light := make([]bool, w*h)
	for y := range h {
		for x := range w {
			light[y*w+x] = Lightness(At(img, b.Min.X+x, b.Min.Y+y)) > l
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

// StrokeRatio is a line of text's stem width over its cap height, in r: pixels nearer fg than bg
// are ink, the cap height is the ink's height, and the stem the median horizontal run of ink no
// longer than a third of it (longer runs are bars and bowls' tops).
func StrokeRatio(img image.Image, r Rect, fg, bg color.NRGBA) (float64, error) {
	b := r.Image().Intersect(img.Bounds())
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
				if c := At(img, x, y); dist(c, fg) < dist(c, bg) {
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

// MarkBox is the extents of r's pixels brighter than level, out of 255, in any channel: an image's
// mark on a dark ground.
func MarkBox(img image.Image, r Rect, level int) (Rect, bool) {
	b := r.Image().Intersect(img.Bounds())
	box := image.Rectangle{}
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := At(img, x, y); int(max(c.R, c.G, c.B)) > level {
				p := image.Rect(x, y, x+1, y+1)
				if found {
					box = box.Union(p)
				} else {
					box, found = p, true
				}
			}
		}
	}
	return Rect{box.Min.X, box.Min.Y, box.Dx(), box.Dy()}, found
}

// FocusDiff compares two screenshots inside r, a before and an after the key focus moved: of the
// pixels that differ beyond delta on a channel, how many are within tol of accent in
// b - the ring drawn - and the share that are in a or in b: the ring leaves the control the focus
// left as it reaches the next, and both are the move.
func FocusDiff(a, b image.Image, r Rect, delta int, accent color.NRGBA, tol int) (drawn int, share float64) {
	rb := r.Image().Intersect(a.Bounds()).Intersect(b.Bounds())
	changed, ring := 0, 0
	for y := rb.Min.Y; y < rb.Max.Y; y++ {
		for x := rb.Min.X; x < rb.Max.X; x++ {
			ca, cb := At(a, x, y), At(b, x, y)
			if ChannelDelta(ca, cb) <= delta {
				continue
			}
			changed++
			if Near(cb, accent, tol) {
				drawn++
				ring++
			} else if Near(ca, accent, tol) {
				ring++
			}
		}
	}
	if changed == 0 {
		return 0, 0
	}
	return drawn, float64(ring) / float64(changed)
}

// ChannelDelta is how far a and b are apart on their furthest channel.
func ChannelDelta(a, b color.NRGBA) int {
	d := 0
	for _, c := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}} {
		v := int(c[0]) - int(c[1])
		if v < 0 {
			v = -v
		}
		d = max(d, v)
	}
	return d
}

// ParseHex reads #RGB, #RGBA, #RRGGBB or #RRGGBBAA.
func ParseHex(s string) (color.NRGBA, bool) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) == 3 || len(h) == 4 {
		var b strings.Builder
		for _, r := range h {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		h = b.String()
	}
	if len(h) == 6 {
		h += "ff"
	}
	if len(h) != 8 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

// Hex is c as #RRGGBB.
func Hex(c color.NRGBA) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

// Hexa is c as #RRGGBBAA, as St reports a colour.
func Hexa(c color.NRGBA) string { return fmt.Sprintf("#%02X%02X%02X%02X", c.R, c.G, c.B, c.A) }
