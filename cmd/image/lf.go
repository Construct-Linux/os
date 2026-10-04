package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The look-and-feel checks, "lf: <rule> <surface>": each rule of the brand's THEMING.md measured
// where it shows - the shell's computed styles through Eval (what St resolved, not what the CSS
// says), pixels inside those extents in the screenshot the harness took of them, and the CSS,
// settings and files on the guest. Needles say whether anything changed; these say whether what
// is drawn is what the rules ask for. Every threshold is a named constant with its reason.
const (
	// A surface's most common colour, against its token: llvmpipe draws flat fills exactly, and
	// 2 leaves room for a dithered gradient's step.
	lfSurfaceTol = 2
	// A 1 px ring, against its token: antialiasing on a fractional allocation blends a little of
	// either neighbour into it.
	lfRingTol = 3
	// How far either side of an extent's edge the ring is looked for.
	lfRingReach = 2
	// Text and tinted fills, against their tokens: glyph hinting and alpha rounding.
	lfTextTol = 4
	// The accent, counted: what is within this of it on every channel reads as the accent, the
	// tints (16-28%) are far outside it.
	lfAccentTol = 14
	// The calendar's today ring is 2 px on a curve: its pixels are half blends of the accent.
	lfRingAccentTol = 40
	// The ring against what is 6 px outside it: the brand's minimum edge contrast.
	lfEdgeContrast = 1.5
	lfEdgeGap      = 6
	// WCAG 2.2 AA for body text.
	lfTextContrast = 4.5
	// Radii, in px of St's computed style.
	lfRadiusTol = 1
	// Cyan pixels a view may show: filled toggles and today put 12,982 and 862 there, measured;
	// tinted, only the text and the marks are the accent.
	lfAccentQuickSettings = 2500
	lfAccentCalendar      = 450
	// Today's ring: at least this share of its outline is the accent.
	lfTodayRing = 0.60
	// The focus ring's A/B: of the pixels focus changed, at least this share are the accent, and
	// at least this many changed at all (a 2 px ring around a toggle is ~800).
	lfFocusAccentShare = 0.5
	lfFocusMinChanged  = 100
	// An expanded submenu is a well, not a slab: no lighter area than this many pixels.
	lfSlabLightness = 0.85
	lfSlabMaxArea   = 400
	// The lock clock is regular: its stem is thinner than this share of its cap height (Adwaita
	// Sans regular ~0.12, bold ~0.19).
	lfClockStemRatio = 0.15
	// The overview's workspace card puts the wallpaper's mark here, at 1280x800.
	lfCardMarkY   = 344
	lfCardMarkTol = 6
	// The top bar's outer height, ring included.
	lfBarHeight = 36
)

// lfPalette is construct-brand's palette.json as the guest has it: colours by token, the numbers
// (tints, the inset, the radii) and the type families.
type lfPalette struct {
	colors map[string]color.NRGBA
	nums   map[string]float64
	strs   map[string]string
}

// The tokens the checks read: a palette without one fails at once, rather than as black.
var (
	lfColorTokens = []string{"brand-boot", "neutral-e", "neutral-f", "neutral-g", "neutral-h", "dark-border",
		"dark-accent", "dark-accent-text", "dark-text", "dark-muted", "light-accent", "light-accent-text"}
	lfNumTokens = []string{"tint-dark-rest", "shape-inset", "shape-radius-control", "shape-radius-surface", "shape-radius-sheet"}
	lfStrTokens = []string{"type-ui", "type-mono"}
)

func parseLFPalette(b []byte) (lfPalette, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return lfPalette{}, fmt.Errorf("%s: %w", brandPalette, err)
	}
	p := lfPalette{colors: map[string]color.NRGBA{}, nums: map[string]float64{}, strs: map[string]string{}}
	for k, v := range raw {
		var s string
		var n float64
		switch {
		case json.Unmarshal(v, &s) == nil:
			if c, ok := parseHex(s); ok && strings.HasPrefix(s, "#") {
				p.colors[k] = c
			} else {
				p.strs[k] = s
			}
		case json.Unmarshal(v, &n) == nil:
			p.nums[k] = n
		}
	}
	var missing []string
	for _, k := range lfColorTokens {
		if _, ok := p.colors[k]; !ok {
			missing = append(missing, k)
		}
	}
	for _, k := range lfNumTokens {
		if _, ok := p.nums[k]; !ok {
			missing = append(missing, k)
		}
	}
	for _, k := range lfStrTokens {
		if _, ok := p.strs[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return p, fmt.Errorf("%s has no %s", brandPalette, strings.Join(missing, ", "))
	}
	return p, nil
}

// has is whether c is one of the palette's colours, by its red, green and blue.
func (p lfPalette) has(c color.NRGBA) bool {
	for _, v := range p.colors {
		if v.R == c.R && v.G == c.G && v.B == c.B {
			return true
		}
	}
	return false
}

// parseHex reads #RGB, #RGBA, #RRGGBB or #RRGGBBAA.
func parseHex(s string) (color.NRGBA, bool) {
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

func hexOf(c color.NRGBA) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

func hexaOf(c color.NRGBA) string { return fmt.Sprintf("#%02X%02X%02X%02X", c.R, c.G, c.B, c.A) }

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

// edgeHit looks for want at (x, y) and up to reach px either way along the normal (nx, ny): a 1 px
// ring lands a pixel to either side of an extent rounded from a fractional allocation.
func edgeHit(img image.Image, x, y, nx, ny, reach int, want color.NRGBA, tol int) (image.Point, bool) {
	for k := 0; k <= reach; k++ {
		for _, s := range []int{k, -k} {
			p := image.Pt(x+s*nx, y+s*ny)
			if p.In(img.Bounds()) && near(at(img, p.X, p.Y), want, tol) {
				return p, true
			}
			if k == 0 {
				break
			}
		}
	}
	return image.Point{}, false
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

// ringShare is the share of points on a pill's outline, inset px inside its extents, within tol of
// want: a 2 px ring drawn inside the edge is sampled down its middle with inset 1. A circle is a
// pill as long as it is wide.
func ringShare(img image.Image, r lfRect, inset float64, want color.NRGBA, tol int) float64 {
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
		if near(at(img, p.X, p.Y), want, tol) {
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
	if channelDelta(inside, outside) < 8 {
		return 0, fmt.Errorf("the window (%s) and what is outside its corner (%s) are too alike to see the corner", hexOf(inside), hexOf(outside))
	}
	dr, dg, db := float64(inside.R)-float64(outside.R), float64(inside.G)-float64(outside.G), float64(inside.B)-float64(outside.B)
	den := dr*dr + dg*dg + db*db
	coverage := func(c color.NRGBA) float64 {
		return ((float64(c.R)-float64(outside.R))*dr + (float64(c.G)-float64(outside.G))*dg + (float64(c.B)-float64(outside.B))*db) / den
	}
	edge := make([]int, maxR)
	for dy := range maxR {
		edge[dy] = maxR
		for dx := range maxR {
			if coverage(at(img, x+dx, y+dy)) >= 0.5 {
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

// focusDiff compares two screenshots inside r: how many pixels differ beyond the needles' channel
// tolerance, and the share of those within tol of accent in b.
func focusDiff(a, b image.Image, r lfRect, accent color.NRGBA, tol int) (int, float64) {
	rb := r.image().Intersect(a.Bounds()).Intersect(b.Bounds())
	changed, cyan := 0, 0
	for y := rb.Min.Y; y < rb.Max.Y; y++ {
		for x := rb.Min.X; x < rb.Max.X; x++ {
			cb := at(b, x, y)
			if channelDelta(at(a, x, y), cb) <= needleChannelDelta {
				continue
			}
			changed++
			if near(cb, accent, tol) {
				cyan++
			}
		}
	}
	if changed == 0 {
		return 0, 0
	}
	return changed, float64(cyan) / float64(changed)
}

// lfPrelude is the JavaScript every lf Eval starts with: look(actor) is what St resolved for an
// actor - its extents on the screen, background, foreground, borders, radii, box-shadow, font,
// and the backgrounds of its ancestors, to composite the surface its text is on; find(root, class)
// the first mapped actor with a style class under root.
const lfPrelude = `
const St = imports.gi.St;
const Gio = imports.gi.Gio;
const lfHex = c => c ? '#' + [c.red, c.green, c.blue, c.alpha].map(v => v.toString(16).padStart(2, '0')).join('').toUpperCase() : '';
const lfBox = a => {
    const [x, y] = a.get_transformed_position();
    const [w, h] = a.get_transformed_size();
    return {x: Math.round(x), y: Math.round(y), w: Math.round(w), h: Math.round(h)};
};
const lfSides = [St.Side.TOP, St.Side.RIGHT, St.Side.BOTTOM, St.Side.LEFT];
const lfCorners = [St.Corner.TOPLEFT, St.Corner.TOPRIGHT, St.Corner.BOTTOMRIGHT, St.Corner.BOTTOMLEFT];
function look(a) {
    if (!a)
        return null;
    const o = {...lfBox(a), mapped: a.is_mapped(), cls: String(a.style_class ?? ''), name: String(a.name ?? '')};
    // St warns (an error in the journal) for a theme node asked of an actor off the stage.
    if (!(a instanceof St.Widget) || !a.get_stage())
        return o;
    const n = a.get_theme_node();
    o.bg = lfHex(n.get_background_color());
    o.fg = lfHex(n.get_foreground_color());
    o.bw = lfSides.map(s => n.get_border_width(s));
    o.bc = lfSides.map(s => lfHex(n.get_border_color(s)));
    o.radius = lfCorners.map(c => n.get_border_radius(c));
    const s = n.get_box_shadow();
    o.shadow = s ? {color: lfHex(s.color), x: s.xoffset, y: s.yoffset, blur: s.blur, spread: s.spread, inset: s.inset} : null;
    const f = n.get_font();
    o.family = f.get_family() ?? '';
    o.weight = f.get_weight();
    if (a instanceof St.Label)
        o.text = a.get_text().replace(/[^ -~]/g, '?').slice(0, 40);
    let insensitive = false, opacity = 1;
    const chain = [];
    for (let p = a; p; p = p.get_parent()) {
        opacity *= p.opacity / 255;
        if (p instanceof St.Widget && p.get_stage()) {
            if (p.has_style_pseudo_class('insensitive'))
                insensitive = true;
            chain.unshift(lfHex(p.get_theme_node().get_background_color()));
        }
    }
    Object.assign(o, {insensitive, opacity, chain});
    return o;
}
function lfAll(root, test) {
    const out = [];
    const walk = a => {
        if (test(a))
            out.push(a);
        for (const c of a.get_children())
            walk(c);
    };
    if (root)
        walk(root);
    return out;
}
const lfClass = cls => a => a instanceof St.Widget && a.has_style_class_name(cls);
function find(root, cls) {
    return lfAll(root, lfClass(cls)).find(a => a.is_mapped()) ?? null;
}
function lfLabels(root) {
    return lfAll(root, a => a instanceof St.Label && a.is_mapped() && a.get_text() !== '').map(look);
}
function lfDock() {
    const panels = global.dashToPanel?.panels ?? [];
    return panels.find(p => p.isPrimary) ?? panels[0] ?? null;
}
function lfWindow(w) {
    if (!w)
        return null;
    const r = w.get_frame_rect();
    return {x: r.x, y: r.y, w: r.width, h: r.height};
}
`

// evalJSON runs body - the inside of an async function, with lfPrelude's helpers - in GNOME Shell
// and decodes what it returns into v. Every character outside printable ASCII, and the quote gdbus
// would switch its quoting for, travel as JSON escapes.
func (g *guest) evalJSON(body string, v any) error {
	js := lfPrelude + `(async () => { ` + body + ` })().then(x => JSON.stringify(x ?? null).replace(/[^ -&(-~]/g, c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0')))`
	raw, err := g.eval(js)
	if err != nil {
		return err
	}
	var inner string
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return fmt.Errorf("eval answered %.80q: %w", raw, err)
	}
	return json.Unmarshal([]byte(inner), v)
}

// lfLook is an actor as look() reports it.
type lfLook struct {
	X, Y, W, H  int
	Mapped      bool      `json:"mapped"`
	Cls         string    `json:"cls"`
	Name        string    `json:"name"`
	BG          string    `json:"bg"`
	FG          string    `json:"fg"`
	BW          []int     `json:"bw"`
	BC          []string  `json:"bc"`
	Radius      []int     `json:"radius"`
	Shadow      *lfShadow `json:"shadow"`
	Family      string    `json:"family"`
	Weight      int       `json:"weight"`
	Text        string    `json:"text"`
	Insensitive bool      `json:"insensitive"`
	Opacity     float64   `json:"opacity"`
	Chain       []string  `json:"chain"`
}

type lfShadow struct {
	Color  string  `json:"color"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Blur   float64 `json:"blur"`
	Spread float64 `json:"spread"`
	Inset  bool    `json:"inset"`
}

func (l *lfLook) rect() lfRect { return lfRect{l.X, l.Y, l.W, l.H} }

// surface is what the actor's own background composites to over its ancestors', from the bottom
// of the stage up: the colour its text is read against.
func (l *lfLook) surface(ground color.NRGBA) color.NRGBA {
	s := ground
	for _, h := range l.Chain {
		if c, ok := parseHex(h); ok {
			s = over(c, s)
		}
	}
	return s
}

// ink is the actor's foreground as drawn: its colour, at its alpha and its ancestors' opacity,
// over its surface.
func (l *lfLook) ink(ground color.NRGBA) color.NRGBA {
	fg, _ := parseHex(l.FG)
	fg.A = uint8(math.Round(float64(fg.A) * l.Opacity))
	return over(fg, l.surface(ground))
}

// lf carries a run's look-and-feel checks: the palette they measure against, and what one view
// measures for a check another view finishes.
type lf struct {
	r   *report
	v   *vm
	g   *guest
	pal lfPalette
	ok  bool
	// The menus' tops, by menu: quick settings and the calendar hang at the same height.
	menuY map[string]int
	// The overview card's mark centre, and the boot's Plymouth mark centre, which must agree.
	cardMark, bootMark *[2]float64
}

// newLF reads the guest's palette.json; without it no lf check can be measured, and one failure
// says why.
func newLF(r *report, v *vm, g *guest) *lf {
	l := &lf{r: r, v: v, g: g, menuY: map[string]int{}}
	raw, err := g.run("cat /"+brandPalette, "")
	if err != nil {
		r.add("lf: palette", false, "/"+brandPalette+" is not on the guest (construct-brand installs it)"+errText(err))
		return l
	}
	if l.pal, err = parseLFPalette([]byte(raw)); err != nil {
		r.add("lf: palette", false, err.Error())
		return l
	}
	l.ok = true
	return l
}

func (l *lf) check(rule, surface string, ok bool, detail string) {
	l.r.add("lf: "+rule+" "+surface, ok, detail)
}

func (l *lf) c(token string) color.NRGBA { return l.pal.colors[token] }

// img is a view's screenshot, as r.shot saved it.
func (l *lf) img(name string) (image.Image, bool) {
	img, err := readPNG(filepath.Join(l.r.dir, name+".png"))
	if err != nil {
		l.check("screenshot", name, false, err.Error())
		return nil, false
	}
	return img, true
}

// view runs the checks that measure the view just shot.
func (l *lf) view(name string) {
	if l == nil || !l.ok {
		return
	}
	img, ok := l.img(name)
	if !ok {
		return
	}
	switch name {
	case "desktop":
		l.bars(name, img, true)
		n := countNear(img, lfRect{0, 0, img.Bounds().Dx(), img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
		l.check("accent", "desktop", n == 0, fmt.Sprintf("%d px of the accent, want none", n))
	case "shell-settings-light":
		l.bars(name, img, false)
		l.settingsLight(img)
	case "shell-quick-settings":
		l.quickSettings(img)
	case "shell-quick-settings-focus":
		l.focus(img)
	case "shell-quick-settings-submenu":
		l.submenu(img)
	case "shell-calendar":
		l.calendar(img)
	case "shell-overview":
		l.overview(img)
	case "shell-overview-search":
		l.search(img)
	case "shell-app-grid":
		l.ground(name, img)
	case "shell-lock-screen":
		l.lock(img)
	case "installer":
		l.installer(img)
	}
}

// surfaceCheck is S1-S3 for one surface: St's background is want and opaque, the pixels inside its
// extents are want, its ring is 1 px of the border colour all round in St and on the screen, and
// the ring stands out from what is 6 px outside it.
func (l *lf) surfaceCheck(name string, a *lfLook, img image.Image, want color.NRGBA) {
	if a == nil || !a.Mapped || a.W <= 0 || a.H <= 0 {
		l.check("surface", name, false, "not on the screen")
		return
	}
	bg, _ := parseHex(a.BG)
	r := a.rect()
	// Inside the ring and clear of the corners' curve.
	rad := min(a.H/2, maxRadius(a))
	inner := lfRect{r.X + max(rad, 3), r.Y + 3, r.W - 2*max(rad, 3), r.H - 6}
	mode, share := modeColor(img, inner)
	l.check("surface", name, bg == color.NRGBA{want.R, want.G, want.B, 255} && near(mode, want, lfSurfaceTol),
		fmt.Sprintf("St %s, drawn %s over %.0f%% of %v; want %s opaque", a.BG, hexOf(mode), share*100, inner, hexOf(want)))
	ring := l.c("dark-border")
	stRing := len(a.BW) == 4 && len(a.BC) == 4
	for i := range 4 {
		if !stRing {
			break
		}
		c, _ := parseHex(a.BC[i])
		stRing = a.BW[i] == 1 && c == color.NRGBA{ring.R, ring.G, ring.B, 255}
	}
	// Each edge's midpoint and its outward normal.
	edges := []struct {
		name         string
		x, y, nx, ny int
	}{
		{"top", r.X + r.W/2, r.Y, 0, -1},
		{"right", r.X + r.W - 1, r.Y + r.H/2, 1, 0},
		{"bottom", r.X + r.W/2, r.Y + r.H - 1, 0, 1},
		{"left", r.X, r.Y + r.H/2, -1, 0},
	}
	var missed, weak []string
	for _, e := range edges {
		p, hit := edgeHit(img, e.x, e.y, e.nx, e.ny, lfRingReach, ring, lfRingTol)
		if !hit {
			missed = append(missed, fmt.Sprintf("%s %s", e.name, hexOf(at(img, e.x, e.y))))
			continue
		}
		o := image.Pt(p.X+e.nx*lfEdgeGap, p.Y+e.ny*lfEdgeGap)
		if !o.In(img.Bounds()) {
			continue
		}
		if c := contrast(at(img, p.X, p.Y), at(img, o.X, o.Y)); c < lfEdgeContrast {
			weak = append(weak, fmt.Sprintf("%s %.2f against %s", e.name, c, hexOf(at(img, o.X, o.Y))))
		}
	}
	l.check("ring", name, stRing && len(missed) == 0,
		fmt.Sprintf("St %v px of %v; edges without %s: %v", a.BW, a.BC, hexOf(ring), missed))
	l.check("edge", name, len(weak) == 0,
		fmt.Sprintf("ring against %d px outside, want >= %.1f: %v", lfEdgeGap, lfEdgeContrast, weak))
}

func maxRadius(a *lfLook) int {
	m := 0
	for _, r := range a.Radius {
		m = max(m, r)
	}
	return m
}

// radiusCheck is S9: every corner of a's computed style is want, within a pixel.
func (l *lf) radiusCheck(name string, a *lfLook, want int) {
	if a == nil || len(a.Radius) != 4 {
		l.check("radius", name, false, "no computed style")
		return
	}
	ok := true
	for _, r := range a.Radius {
		ok = ok && abs(r-want) <= lfRadiusTol
	}
	l.check("radius", name, ok, fmt.Sprintf("%v, want %d", a.Radius, want))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// bars checks the top bar and the dock, S1-S3: opaque g with a ring, in the dark desktop and in
// the light scheme alike (the shell is dark in both). On the desktop it measures their shape and
// placement too, and the text of the top bar.
func (l *lf) bars(view string, img image.Image, desktop bool) {
	var s struct {
		Panel, Dock, Menu, Banner *lfLook
		Labels                    []*lfLook
	}
	err := l.g.evalJSON(`
		const menu = Object.values(Main.panel.statusArea).map(i => i.menu?.box).find(b => b instanceof St.Widget &&
			b.has_style_class_name('popup-menu-content') && !b.has_style_class_name('quick-settings') && !b.has_style_class_name('datemenu-popover'));
		// A banner exists only while a notification shows: one like it, styled as the theme styles it.
		const banner = new St.Bin({style_class: 'message notification-banner'});
		Main.uiGroup.add_child(banner);
		const b = look(banner);
		banner.destroy();
		return {panel: look(Main.panel), dock: look(lfDock()), menu: look(menu), banner: b, labels: lfLabels(Main.panel)};`, &s)
	if err != nil {
		l.check("bars", view, false, err.Error())
		return
	}
	g := l.c("neutral-g")
	l.surfaceCheck("top bar ("+view+")", s.Panel, img, g)
	l.surfaceCheck("dock ("+view+")", s.Dock, img, g)
	if !desktop || s.Panel == nil || s.Dock == nil {
		return
	}
	surface := int(l.pal.nums["shape-radius-surface"])
	l.radiusCheck("dock", s.Dock, surface)
	l.radiusCheck("popup menu", s.Menu, surface)
	l.radiusCheck("banner", s.Banner, surface)
	pill := s.Panel.H > 0 && len(s.Panel.Radius) == 4
	for _, r := range s.Panel.Radius {
		pill = pill && r*2 >= s.Panel.H
	}
	l.check("radius", "top bar", pill, fmt.Sprintf("%v, want at least half its %d px height", s.Panel.Radius, s.Panel.H))
	inset := int(l.pal.nums["shape-inset"])
	gap := img.Bounds().Dy() - (s.Dock.Y + s.Dock.H)
	l.check("spacing", "bars", s.Panel.Y == inset && abs(gap-inset) <= 1 && s.Panel.H == lfBarHeight,
		fmt.Sprintf("top bar at y %d, %d px high; dock %d px off the bottom; want %d, %d and %d", s.Panel.Y, s.Panel.H, gap, inset, lfBarHeight, inset))
	family := l.pal.strs["type-ui"]
	var off []string
	for _, t := range s.Labels {
		if t.Weight != 400 || firstFamily(t.Family) != family {
			off = append(off, fmt.Sprintf("%q %s %d", t.Text, t.Family, t.Weight))
		}
	}
	l.check("type", "top bar", len(s.Labels) > 0 && len(off) == 0,
		fmt.Sprintf("%d labels; not %s 400: %v", len(s.Labels), family, off))
}

// firstFamily is a font family list's first, unquoted.
func firstFamily(f string) string {
	first, _, _ := strings.Cut(f, ",")
	return strings.Trim(strings.TrimSpace(first), `"'`)
}

// labelsCheck is S7: every label on screen under a surface reads at WCAG AA on what it is drawn
// on, unless it is insensitive.
func (l *lf) labelsCheck(name string, labels []*lfLook) {
	ground := l.c("neutral-e")
	var low []string
	n := 0
	for _, t := range labels {
		if t.Insensitive || t.W == 0 || t.H == 0 {
			continue
		}
		n++
		if c := contrast(t.ink(ground), t.surface(ground)); c < lfTextContrast {
			low = append(low, fmt.Sprintf("%q %s on %s %.2f", t.Text, hexOf(t.ink(ground)), hexOf(t.surface(ground)), c))
		}
	}
	l.check("text", name, n > 0 && len(low) == 0, fmt.Sprintf("%d labels; under %.1f: %v", n, lfTextContrast, low))
}

// quickToggle is a quick toggle as the checks see it: the button, its title and its subtitle.
type quickToggle struct {
	Look     *lfLook `json:"look"`
	Checked  bool    `json:"checked"`
	Title    *lfLook `json:"title"`
	Subtitle *lfLook `json:"subtitle"`
}

const lfQuickSettings = `const m = Main.panel.statusArea.quickSettings.menu;`

func (l *lf) quickSettings(img image.Image) {
	var s struct {
		Box     *lfLook
		Toggles []quickToggle
		Labels  []*lfLook
	}
	if err := l.g.evalJSON(lfQuickSettings+`
		const toggles = lfAll(m.box, lfClass('quick-toggle')).filter(a => a.is_mapped()).map(a => ({
			look: look(a), checked: a.checked, title: look(a._title), subtitle: a._subtitle?.visible ? look(a._subtitle) : null}));
		return {box: look(m.box), toggles, labels: lfLabels(m.box)};`, &s); err != nil {
		l.check("quick settings", "eval", false, err.Error())
		return
	}
	h := l.c("neutral-h")
	l.surfaceCheck("quick settings", s.Box, img, h)
	l.radiusCheck("quick settings", s.Box, 40)
	if s.Box != nil {
		l.menuY["quick settings"] = s.Box.Y
	}
	n := countNear(img, lfRect{0, 0, img.Bounds().Dx(), img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
	l.check("accent", "quick settings", n < lfAccentQuickSettings, fmt.Sprintf("%d px of the accent, want under %d", n, lfAccentQuickSettings))
	fill := tint(l.c("dark-accent"), h, l.pal.nums["tint-dark-rest"])
	ground := l.c("neutral-e")
	var on, off int
	var bad []string
	for _, t := range s.Toggles {
		if t.Look == nil || t.Title == nil {
			continue
		}
		if t.Checked {
			on++
			mode, _ := modeColor(img, t.Look.rect().inset(4, 4))
			if !near(mode, fill, lfTextTol) {
				bad = append(bad, fmt.Sprintf("%q on is %s, not the tint %s", t.Title.Text, hexOf(mode), hexOf(fill)))
			}
			if c := t.Title.ink(ground); !near(c, l.c("dark-accent-text"), lfTextTol) || countNear(img, t.Title.rect(), l.c("dark-accent-text"), lfRingAccentTol) == 0 {
				bad = append(bad, fmt.Sprintf("%q on titled %s, not the accent", t.Title.Text, hexOf(c)))
			}
			if t.Subtitle != nil && !near(t.Subtitle.ink(ground), l.c("dark-text"), lfTextTol) {
				bad = append(bad, fmt.Sprintf("%q on subtitled %s, not text: muted never goes on a tint", t.Title.Text, hexOf(t.Subtitle.ink(ground))))
			}
			continue
		}
		off++
		if c := t.Title.ink(ground); !near(c, l.c("dark-text"), lfTextTol) {
			bad = append(bad, fmt.Sprintf("%q off titled %s, not text", t.Title.Text, hexOf(c)))
		}
		if t.Subtitle != nil && !near(t.Subtitle.ink(ground), l.c("dark-muted"), lfTextTol) {
			bad = append(bad, fmt.Sprintf("%q off subtitled %s, not muted", t.Title.Text, hexOf(t.Subtitle.ink(ground))))
		}
	}
	l.check("toggles", "quick settings", on > 0 && off > 0 && len(bad) == 0, fmt.Sprintf("%d on, %d off: %v", on, off, bad))
	l.labelsCheck("quick settings", s.Labels)
}

// focus is S8: the first quick toggle, given the key focus, shows the accent's inset ring, and the
// ring is what focus changed on the screen.
func (l *lf) focus(img image.Image) {
	var s struct {
		Toggle  *lfLook
		Focused bool
	}
	if err := l.g.evalJSON(lfQuickSettings+`
		const t = find(m.box, 'quick-toggle');
		return {toggle: look(t), focused: t !== null && global.stage.key_focus === t};`, &s); err != nil {
		l.check("focus", "quick toggle", false, err.Error())
		return
	}
	if s.Toggle == nil || !s.Focused {
		l.check("focus", "quick toggle", false, fmt.Sprintf("the first quick toggle did not take the key focus (found: %v)", s.Toggle != nil))
		return
	}
	accent := l.c("dark-accent")
	sh := s.Toggle.Shadow
	shOK := false
	detail := "no box-shadow"
	if sh != nil {
		c, _ := parseHex(sh.Color)
		shOK = sh.Inset && sh.Spread >= 2 && near(c, accent, 0) && c.A == 255
		detail = fmt.Sprintf("box-shadow %s inset=%v spread %.0f", sh.Color, sh.Inset, sh.Spread)
	}
	l.check("focus", "quick toggle style", shOK, detail+"; want inset, spread >= 2, "+hexOf(accent))
	before, ok := l.img("shell-quick-settings")
	if !ok {
		return
	}
	r := s.Toggle.rect().inset(-2, -2)
	changed, share := focusDiff(before, img, r, accent, lfRingAccentTol)
	l.check("focus", "quick toggle drawn", changed >= lfFocusMinChanged && share >= lfFocusAccentShare,
		fmt.Sprintf("%d px changed in %v against shell-quick-settings, %.0f%% of them the accent; want >= %d and >= %.0f%%",
			changed, r, share*100, lfFocusMinChanged, lfFocusAccentShare*100))
}

// submenu is the expanded submenu: a g well in the h menu, with no light slab in it.
func (l *lf) submenu(img image.Image) {
	var s struct {
		Box  *lfLook
		Open bool
	}
	if err := l.g.evalJSON(lfQuickSettings+`
		return {box: look(m.box), open: find(m.box, 'quick-toggle-menu') !== null};`, &s); err != nil || s.Box == nil {
		l.check("submenu", "quick settings", false, fmt.Sprintf("no quick settings box%s", errText(err)))
		return
	}
	area := lightSlab(img, s.Box.rect(), lfSlabLightness)
	l.check("submenu", "quick settings", s.Open && area <= lfSlabMaxArea,
		fmt.Sprintf("a submenu open: %v; largest area lighter than %.0f%%: %d px, want <= %d", s.Open, lfSlabLightness*100, area, lfSlabMaxArea))
}

func (l *lf) calendar(img image.Image) {
	var s struct {
		Box, Today, Day *lfLook
		Selected        bool
		Labels          []*lfLook
	}
	if err := l.g.evalJSON(`
		const dm = Main.panel.statusArea.dateMenu;
		const today = (dm._calendar._buttons ?? []).find(b => b.has_style_class_name('calendar-today'));
		return {box: look(dm.menu.box), today: look(today), selected: today?.has_style_pseudo_class('selected') ?? false,
			day: look(dm._date?._dayLabel), labels: lfLabels(dm.menu.box)};`, &s); err != nil {
		l.check("calendar", "eval", false, err.Error())
		return
	}
	h := l.c("neutral-h")
	l.surfaceCheck("calendar", s.Box, img, h)
	l.radiusCheck("calendar", s.Box, int(l.pal.nums["shape-radius-sheet"]))
	if s.Box != nil {
		l.menuY["calendar"] = s.Box.Y
	}
	n := countNear(img, lfRect{0, 0, img.Bounds().Dx(), img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
	l.check("accent", "calendar", n < lfAccentCalendar, fmt.Sprintf("%d px of the accent, want under %d", n, lfAccentCalendar))
	if s.Today == nil || s.Today.W == 0 {
		l.check("today", "calendar", false, "no day styled calendar-today")
	} else {
		// The ring is 2 px inside the edge; the number is in the middle. The menu opens on today, so
		// it is the selected day too, which takes the accent's resting tint inside the ring.
		mode, _ := modeColor(img, s.Today.rect().inset(4, 4))
		centre := h
		if s.Selected {
			centre = tint(l.c("dark-accent"), h, l.pal.nums["tint-dark-rest"])
		}
		share := ringShare(img, s.Today.rect(), 1, l.c("dark-accent"), lfRingAccentTol)
		l.check("today", "calendar", near(mode, centre, lfTextTol) && share >= lfTodayRing,
			fmt.Sprintf("centre %s, want %s (selected: %v); %.0f%% of its ring the accent, want >= %.0f%%", hexOf(mode), hexOf(centre), s.Selected, share*100, lfTodayRing*100))
	}
	if s.Day == nil {
		l.check("text", "calendar day", false, "no day label")
	} else {
		c := s.Day.ink(l.c("neutral-e"))
		l.check("text", "calendar day", near(c, l.c("dark-text"), lfTextTol), fmt.Sprintf("%s, want %s", hexOf(c), hexOf(l.c("dark-text"))))
	}
	l.labelsCheck("calendar", s.Labels)
	if q, ok := l.menuY["quick settings"]; ok && s.Box != nil {
		l.check("spacing", "menus", abs(q-s.Box.Y) <= 1, fmt.Sprintf("quick settings at y %d, the calendar at %d", q, s.Box.Y))
	}
}

// ground is the overview's and the app grid's: the boot's black.
func (l *lf) ground(view string, img image.Image) {
	c := at(img, 5, 100)
	l.check("ground", view, near(c, l.c("brand-boot"), lfSurfaceTol), fmt.Sprintf("(5,100) is %s, want %s", hexOf(c), hexOf(l.c("brand-boot"))))
}

func (l *lf) overview(img image.Image) {
	l.ground("shell-overview", img)
	var s struct{ Group, Card *lfLook }
	if err := l.g.evalJSON(`
		const cards = lfAll(Main.layoutManager.overviewGroup, lfClass('workspace-background')).filter(a => a.is_mapped());
		cards.sort((a, b) => b.width - a.width);
		return {group: look(Main.layoutManager.overviewGroup), card: look(cards[0])};`, &s); err != nil {
		l.check("ground", "overview", false, err.Error())
		return
	}
	if s.Group != nil {
		l.check("ground", "overviewGroup", s.Group.BG == hexaOf(l.c("brand-boot")), "St "+s.Group.BG)
	}
	if s.Card == nil || s.Card.W == 0 {
		l.check("mark", "overview card", false, "no workspace card on the screen")
		return
	}
	box, ok := markBox(img, s.Card.rect().inset(8, 8))
	if !ok {
		l.check("mark", "overview card", false, "no mark in the card "+s.Card.rect().String())
		return
	}
	cx, cy := float64(box.X)+float64(box.W)/2, float64(box.Y)+float64(box.H)/2
	l.cardMark = &[2]float64{cx, cy}
	l.check("mark", "overview card", math.Abs(cy-lfCardMarkY) <= lfCardMarkTol,
		fmt.Sprintf("centred at (%.0f, %.0f), want y %d±%d", cx, cy, lfCardMarkY, lfCardMarkTol))
}

func (l *lf) search(img image.Image) {
	var s struct{ Entry, Section *lfLook }
	if err := l.g.evalJSON(`
		return {entry: look(Main.overview.searchEntry), section: look(find(Main.layoutManager.overviewGroup, 'search-section-content'))};`, &s); err != nil {
		l.check("search", "overview", false, err.Error())
		return
	}
	h := l.c("neutral-h")
	l.surfaceCheck("search entry", s.Entry, img, h)
	l.surfaceCheck("search section", s.Section, img, h)
	// Concentric: a control's radius plus the section's padding.
	l.radiusCheck("search section", s.Section, int(l.pal.nums["shape-radius-control"])+12)
}

func (l *lf) lock(img image.Image) {
	var s struct {
		Group, Time *lfLook
		URI         string
	}
	if err := l.g.evalJSON(`
		const css = St.ThemeContext.get_for_stage(global.stage).get_theme().default_stylesheet;
		return {group: look(Main.screenShield._lockDialogGroup), time: look(Main.screenShield._dialog?._clock?._time), uri: css ? css.get_uri() : ''};`, &s); err != nil {
		l.check("lock", "eval", false, err.Error())
		return
	}
	if s.Group == nil {
		l.check("ground", "lock", false, "no lockDialogGroup")
	} else {
		l.check("ground", "lock", s.Group.BG == hexaOf(l.c("brand-boot")), "St "+s.Group.BG)
	}
	const want = "resource:///org/gnome/shell/theme/gnome-shell-dark.css"
	l.check("theme", "lock", s.URI == want, fmt.Sprintf("the shell's default stylesheet is %q, want %s (orchis-theme-shell's gnome-shell-theme.gresource)", s.URI, want))
	if s.Time == nil || s.Time.W == 0 {
		l.check("type", "lock clock", false, "no clock on the lock screen")
		return
	}
	fg, _ := parseHex(s.Time.FG)
	// The label's top rows are its padding, clear of the digits.
	bg, _ := modeColor(img, lfRect{s.Time.X, s.Time.Y, s.Time.W, 3})
	ratio, err := strokeRatio(img, s.Time.rect(), fg, bg)
	l.check("type", "lock clock", err == nil && ratio < lfClockStemRatio,
		fmt.Sprintf("stem/cap %.3f, want under %.2f (regular, not bold)%s", ratio, lfClockStemRatio, errText(err)))
}

// focusWindow is the window with the focus: its frame on the screen and its class.
func (l *lf) focusWindow() (lfRect, string, error) {
	var s struct {
		Win *struct{ X, Y, W, H int }
		Cls string
	}
	if err := l.g.evalJSON(`const w = global.display.focus_window; return {win: lfWindow(w), cls: w?.get_wm_class() ?? ''};`, &s); err != nil {
		return lfRect{}, "", err
	}
	if s.Win == nil {
		return lfRect{}, "", errors.New("no window has the focus")
	}
	return lfRect{s.Win.X, s.Win.Y, s.Win.W, s.Win.H}, s.Cls, nil
}

// installer is S4 on the installer's window: its body is f, its left and bottom edges the ring,
// its headerbar g.
func (l *lf) installer(img image.Image) {
	r, cls, err := l.focusWindow()
	if err != nil || cls != "io.github.construct_linux.Installer" {
		l.check("window", "installer", false, fmt.Sprintf("the focused window is %q, not the installer%s", cls, errText(err)))
		return
	}
	body, _ := modeColor(img, lfRect{630, 590, 21, 21})
	ring := l.c("dark-border")
	_, left := edgeHit(img, r.X-1, r.Y+r.H/2, -1, 0, lfRingReach, ring, lfRingTol)
	_, bottom := edgeHit(img, r.X+r.W/2, r.Y+r.H, 0, 1, lfRingReach, ring, lfRingTol)
	head, _ := modeColor(img, lfRect{r.X + 8, r.Y + 3, r.W - 16, 8})
	l.check("window", "installer", near(body, l.c("neutral-f"), lfSurfaceTol) && left && bottom && near(head, l.c("neutral-g"), lfSurfaceTol),
		fmt.Sprintf("frame %v: body %s (want %s), ring left %v bottom %v, headerbar %s (want %s)",
			r, hexOf(body), hexOf(l.c("neutral-f")), left, bottom, hexOf(head), hexOf(l.c("neutral-g"))))
}

func (l *lf) settingsLight(img image.Image) {
	var s struct{ Win *struct{ X, Y, W, H int } }
	if err := l.g.evalJSON(`return {win: lfWindow(Shell.AppSystem.get_default().lookup_app('org.gnome.Settings.desktop')?.get_windows()[0])};`, &s); err != nil || s.Win == nil {
		l.check("radius", "settings-light window", false, "no Settings window"+errText(err))
		return
	}
	want := int(l.pal.nums["shape-radius-surface"])
	r, err := cornerRadius(img, s.Win.X, s.Win.Y, 2*want)
	l.check("radius", "settings-light window", err == nil && math.Abs(r-float64(want)) <= lfRadiusTol,
		fmt.Sprintf("its top-left corner fits a %.2f px radius, want %d%s", r, want, errText(err)))
	l.r.skip("lf: radius settings-light card", "a card's extents come only from AT-SPI, which the harness does not read yet")
}

// appWindow is the frame of an app's first window.
func (l *lf) appWindow(id string) (lfRect, error) {
	var s struct{ Win *struct{ X, Y, W, H int } }
	if err := l.g.evalJSON(fmt.Sprintf(`return {win: lfWindow(Shell.AppSystem.get_default().lookup_app(%q)?.get_windows()[0])};`, id), &s); err != nil {
		return lfRect{}, err
	}
	if s.Win == nil {
		return lfRect{}, errors.New("no window")
	}
	return lfRect{s.Win.X, s.Win.Y, s.Win.W, s.Win.H}, nil
}

// app checks the apps outside GTK that take the desktop's surfaces: Ghostty and VS Code on g and
// f, Chrome off its own dark grey. Firefox's form controls are checked on a page of their own.
func (l *lf) app(id, shot string) {
	if l == nil || !l.ok {
		return
	}
	switch id {
	case "com.mitchellh.ghostty.desktop", "code.desktop", "com.google.Chrome.desktop", "firefox.desktop":
	default:
		return
	}
	img, ok := l.img(shot)
	if !ok {
		return
	}
	r, err := l.appWindow(id)
	if err != nil {
		l.check("app", shot, false, err.Error())
		return
	}
	g, f := l.c("neutral-g"), l.c("neutral-f")
	region := func(x, y, w, h int) color.NRGBA {
		c, _ := modeColor(img, lfRect{r.X + x, r.Y + y, w, h})
		return c
	}
	switch id {
	case "com.mitchellh.ghostty.desktop":
		head := region(8, 3, r.W-16, 8)
		term := region(r.W/4, r.H*2/3, r.W/2, r.H/4)
		// Ghostty's own default background, which a missing theme leaves.
		stock := countNear(img, r, color.NRGBA{0x28, 0x2C, 0x34, 255}, lfSurfaceTol)
		l.check("app", "ghostty", near(head, g, lfSurfaceTol) && near(term, g, lfSurfaceTol) && stock == 0,
			fmt.Sprintf("headerbar %s, terminal %s, want %s; %d px of #282C34", hexOf(head), hexOf(term), hexOf(g), stock))
	case "code.desktop":
		title := region(8, 4, r.W-16, 16)
		editor := region(r.W/2, r.H/3, r.W/3, r.H/3)
		side := region(4, r.H/3, 36, r.H/3)
		status := region(8, r.H-18, r.W-16, 12)
		l.check("app", "vscode", near(title, g, lfSurfaceTol) && near(editor, g, lfSurfaceTol) && near(side, f, lfSurfaceTol) && near(status, f, lfSurfaceTol),
			fmt.Sprintf("title bar %s and editor %s want %s; side %s and status bar %s want %s",
				hexOf(title), hexOf(editor), hexOf(g), hexOf(side), hexOf(status), hexOf(f)))
	case "com.google.Chrome.desktop":
		strip := region(8, 3, r.W-16, 10)
		stock := color.NRGBA{0x20, 0x21, 0x24, 255}
		l.check("app", "chrome", !near(strip, stock, lfSurfaceTol), fmt.Sprintf("tab strip %s, want not Chrome's own %s", hexOf(strip), hexOf(stock)))
	case "firefox.desktop":
		l.firefoxControls(id, r)
	}
}

// firefoxPage is a light page of the controls GTK draws with the accent: Firefox takes them from
// the theme for the other scheme, which without an Orchis-Construct it finds as Adwaita's blue.
const firefoxPage = `<meta name="color-scheme" content="light"><body style="margin:48px;font:16px sans-serif">` +
	`<p><input type="checkbox" checked> <input type="radio" checked> <input type="range"> ` +
	`<progress value="60" max="100"></progress> <button>Button</button> <input value="text" autofocus></p>`

func (l *lf) firefoxControls(id string, win lfRect) {
	uri := "data:text/html;charset=utf-8," + url.PathEscape(firefoxPage)
	if err := l.g.evalJSON(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q).get_app_info().launch_uris([%q], null); return true;`, id, uri), new(bool)); err != nil {
		l.check("app", "firefox controls", false, err.Error())
		return
	}
	time.Sleep(6 * time.Second)
	l.r.shotLook(l.v, l.g, "firefox-controls")
	img, ok := l.img("firefox-controls")
	if !ok {
		return
	}
	blue := color.NRGBA{0x35, 0x84, 0xE4, 255}
	n := countNear(img, win, blue, 8)
	accent := countNear(img, win, l.c("light-accent"), 12)
	l.check("app", "firefox controls", n == 0, fmt.Sprintf("%d px of Adwaita's #3584E4 on a light page, want none (%d px of %s)", n, accent, hexOf(l.c("light-accent"))))
}

// finish checks what spans views: the boot's mark where the overview's card puts it.
func (l *lf) finish() {
	if l == nil || !l.ok || l.bootMark == nil || l.cardMark == nil {
		return
	}
	dx, dy := math.Abs(l.bootMark[0]-l.cardMark[0]), math.Abs(l.bootMark[1]-l.cardMark[1])
	l.check("mark", "boot to overview", dx <= 6 && dy <= 12,
		fmt.Sprintf("Plymouth's mark at (%.0f, %.0f), the overview card's at (%.0f, %.0f): want within 6 px across, 12 down",
			l.bootMark[0], l.bootMark[1], l.cardMark[0], l.cardMark[1]))
}

// gtkViews are libadwaita's own surfaces the shell views do not reach: a popover, a tooltip and a
// dialog, in Text Editor in the dark scheme. They are screenshots for the needles; each passes
// when what opens it worked.
func (r *report) gtkViews(v *vm, g *guest) {
	const id = "org.gnome.TextEditor.desktop"
	if ok, detail := g.runApp(app{id, 20 * time.Second}); !ok {
		r.add("view:gtk", false, detail)
		return
	}
	time.Sleep(3 * time.Second)
	windows := func() int {
		n, _ := g.eval(`global.get_window_actors().length`)
		k, _ := strconv.Atoi(n)
		return k
	}
	var frame struct{ X, Y, W, H int }
	if err := g.evalJSON(fmt.Sprintf(`const w = Shell.AppSystem.get_default().lookup_app(%q).get_windows()[0];
		w.activate(global.get_current_time()); return lfWindow(w);`, id), &frame); err != nil {
		r.add("view:gtk", false, err.Error())
		return
	}
	time.Sleep(time.Second)
	// F10 opens a GTK window's primary menu: a popover, its own surface.
	before := windows()
	_ = v.key("f10")
	time.Sleep(2 * time.Second)
	r.add("view:gtk-popover", windows() > before, fmt.Sprintf("%d surfaces, %d before F10", windows(), before))
	r.shotLook(v, g, "gtk-popover")
	_ = v.key("esc")
	time.Sleep(time.Second)
	// The pointer over the close button, the headerbar's last, until GTK shows its tooltip.
	before = windows()
	sw, sh := 1280, 800
	x, y := frame.X+frame.W-22, frame.Y+23
	_ = v.pointTo(x*32767/(sw-1), y*32767/(sh-1))
	time.Sleep(3 * time.Second)
	r.add("view:gtk-tooltip", windows() > before, fmt.Sprintf("%d surfaces with the pointer at (%d, %d), %d before", windows(), x, y, before))
	r.shotLook(v, g, "gtk-tooltip")
	_ = v.pointTo(16000, 30000)
	time.Sleep(time.Second)
	// A libadwaita dialog, opened by the app's own action over its bus: preferences, else about.
	out, err := g.run(`b='sudo -u live env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus gdbus call --session --dest org.gnome.TextEditor --object-path /org/gnome/TextEditor --method org.gtk.Actions.Activate'; `+
		`$b preferences '[]' '{}' >/dev/null 2>&1 && echo preferences || { $b about '[]' '{}' 2>&1 && echo about; }`, "")
	time.Sleep(2 * time.Second)
	r.add("view:gtk-dialog", err == nil && (strings.Contains(out, "preferences") || strings.Contains(out, "about")), "app action: "+oneLine(out)+errText(err))
	r.shotLook(v, g, "gtk-dialog")
	_ = v.key("esc")
	_, _ = g.eval(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q)?.request_quit(); true`, id))
	time.Sleep(2 * time.Second)
}

// guestLook is the checks on the guest itself: the shell's CSS as the running shell loaded it,
// the theme's resources, the fonts and settings, and the files other parts install.
func (l *lf) guestLook() {
	if l == nil || !l.ok {
		return
	}
	var s struct {
		URI, CSS   string
		ToggleOn   bool
		Background string
		Icons      []struct{ ID, Icon, File string }
	}
	err := l.g.evalJSON(`
		const css = St.ThemeContext.get_for_stage(global.stage).get_theme().default_stylesheet;
		let text = '';
		if (css) {
			const [, bytes] = css.load_contents(null);
			text = new TextDecoder().decode(bytes);
		}
		let toggleOn = true;
		try {
			Gio.resources_get_info('/org/gnome/shell/theme/assets/toggle-on.svg', 0);
		} catch (e) {
			toggleOn = false;
		}
		let bg = '';
		try {
			bg = new TextDecoder().decode(Gio.resources_lookup_data('/org/gnome/shell/ui/background.js', 0).toArray());
		} catch (e) {
			bg = 'background.js not in the shell resources: ' + e;
		}
		const theme = new St.IconTheme();
		const icons = Gio.AppInfo.get_all().filter(a => a.should_show()).map(a => {
			const i = a.get_icon();
			const info = i ? theme.lookup_by_gicon(i, 48, 0) : null;
			return {id: a.get_id() ?? '', icon: i ? i.to_string() : '', file: info ? info.get_filename() ?? '' : ''};
		});
		return {uri: css ? css.get_uri() : '', css: text, toggleOn, background: bg.split('\n').find(l => l.includes('DEFAULT_BACKGROUND_COLOR =')) ?? bg.slice(0, 160), icons};`, &s)
	if err != nil {
		l.check("guest", "shell", false, err.Error())
		return
	}
	if s.CSS == "" {
		l.check("css", "shell", false, "the shell has no default stylesheet")
	} else {
		rules := parseCSS(s.CSS)
		ok, detail := cssContract(rules, s.CSS, l.pal)
		l.check("css contract", "G1", ok, detail)
		ok, detail = cssFocus(rules, l.c("dark-accent"))
		l.check("css focus", "G2", ok, detail)
		ok, detail = cssPalette(rules, l.pal)
		l.check("css palette", "G3", ok, detail)
		ok, detail = cssWeights(rules)
		l.check("css weights", "G6", ok, detail)
	}
	l.check("gresource", "toggle-on", s.ToggleOn, "/org/gnome/shell/theme/assets/toggle-on.svg in the shell's resources (orchis-theme-shell)")
	l.check("startup stage", "background.js", strings.Contains(s.Background, "red: 0, green: 0, blue: 0"), "gnome-shell's "+strings.TrimSpace(s.Background))
	var unresolved []string
	for _, a := range s.Icons {
		f := a.File
		themed := strings.Contains(f, "/icons/Tela-dark/") || strings.Contains(f, "/icons/Tela/") ||
			(strings.Contains(f, "/icons/hicolor/") && strings.HasPrefix(filepath.Base(f), "io.github.construct_linux."))
		if !themed {
			unresolved = append(unresolved, fmt.Sprintf("%s (%s -> %q)", a.ID, a.Icon, f))
		}
	}
	l.check("icons", "apps", len(s.Icons) > 0 && len(unresolved) == 0, fmt.Sprintf("%d apps shown; not drawn by Tela or a brand tile: %v", len(s.Icons), unresolved))

	user := `sudo -u live env HOME=/var/home/live XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus `
	fonts, err := l.g.run(user+`gsettings get org.gnome.desktop.interface font-name; `+user+`gsettings get org.gnome.desktop.interface monospace-font-name; `+
		`for f in monospace sans-serif system-ui; do fc-match -f '%{family[0]}\n' $f; done`, "")
	ui, mono := l.pal.strs["type-ui"], l.pal.strs["type-mono"]
	want := fmt.Sprintf("'%s 11'\n'%s 11'\n%s\n%s\n%s\n", ui, mono, mono, ui, ui)
	l.check("fonts", "G4", err == nil && fonts == want, fmt.Sprintf("font-name, monospace-font-name, fc-match monospace/sans-serif/system-ui: %s; want %s", oneLine(fonts), oneLine(want))+errText(err))

	dconf, err := l.g.run(`for k in dash-to-panel/focus-highlight-color dash-to-panel/focus-highlight-opacity dash-to-panel/trans-use-border tilingshell/window-border-color; do sudo -u live dconf read /org/gnome/shell/extensions/$k; done`, "")
	accent := hexOf(l.c("dark-accent"))
	wantDconf := fmt.Sprintf("'%s'\n14\nfalse\n'%s'\n", accent, accent)
	l.check("extensions", "G5 settings", err == nil && strings.EqualFold(dconf, wantDconf), fmt.Sprintf("%s; want %s", oneLine(dconf), oneLine(wantDconf))+errText(err))
	for _, uuid := range []string{"dash-to-panel@jderose9.github.com", "tilingshell@ferrarodomenico.com", "Vitals@CoreCoding.com"} {
		css, err := l.g.run("cat /usr/share/gnome-shell/extensions/"+uuid+"/stylesheet.css", "")
		if err != nil {
			l.check("extensions", "G5 "+uuid, false, "no stylesheet.css"+errText(err))
			continue
		}
		ok, detail := cssPalette(parseCSS(css), l.pal)
		l.check("extensions", "G5 "+uuid, ok, detail)
	}

	gtk4, err := l.g.run(`grep -oiE -- '--accent-color: *#[0-9a-f]+' /usr/share/themes/Orchis-Construct-Light/gtk-4.0/gtk.css | head -1`, "")
	got := strings.TrimSpace(gtk4[strings.LastIndex(gtk4, "#")+1:])
	l.check("gtk4", "light accent", err == nil && strings.EqualFold("#"+got, hexOf(l.c("light-accent-text"))),
		fmt.Sprintf("Orchis-Construct-Light's gtk-4.0 %s, want --accent-color: %s", oneLine(gtk4), hexOf(l.c("light-accent-text")))+errText(err))
	link, err := l.g.run(`readlink /usr/share/themes/Orchis-Construct && test -f /usr/share/themes/Orchis-Construct/gtk-3.0/gtk.css && echo gtk-3.0`, "")
	l.check("firefox theme", "Orchis-Construct", err == nil && link == "Orchis-Construct-Light\ngtk-3.0\n", "link to "+oneLine(link)+errText(err))
	ghostty, err := l.g.run(`test -s "/usr/share/ghostty/themes/Construct Dark" && test -s "/usr/share/ghostty/themes/Construct Light" && echo themes; `+
		`test -s /var/home/live/.config/ghostty/config.ghostty && echo seed; `+
		`out=$(`+user+`ghostty +validate-config 2>&1); rc=$?; echo "$out" | tail -3; echo "rc=$rc"`, "")
	l.check("ghostty", "themes and seed", err == nil && strings.Contains(ghostty, "themes\nseed\n") && strings.Contains(ghostty, "rc=0"), oneLine(ghostty)+errText(err))
	code, err := l.g.run(`cd /usr/share/code/resources/app/extensions/construct-theme && python3 -c 'import json,glob; fs=["package.json"]+sorted(glob.glob("themes/*.json")); [json.load(open(f)) for f in fs]; print(len(fs))'`, "")
	parsed, _ := strconv.Atoi(strings.TrimSpace(code))
	// package.json and the two themes, dark and light.
	l.check("vscode", "construct-theme", err == nil && parsed >= 3, "JSON files that parse: "+oneLine(code)+errText(err))
	entries, err := l.g.run(`for f in /usr/share/applications/*.desktop /var/lib/flatpak/exports/share/applications/*.desktop /var/home/live/.local/share/applications/*.desktop; do `+
		`[ -f "$f" ] || continue; grep -qE '^(NoDisplay|Hidden)=true' "$f" && continue; grep -q '^Icon=/' "$f" && echo "icon:$f"; case "$f" in *[Ww]olfi*.desktop) echo "wolfi:$f";; esac; done; true`, "")
	l.check("entries", "icons and wolfi", err == nil && strings.TrimSpace(entries) == "", "absolute Icon= or a distrobox entry: "+oneLine(entries)+errText(err))
}

// cssRule is one rule of a stylesheet: its selectors, and its declarations in order, each
// property lowercased and its value without !important.
type cssRule struct {
	selectors []string
	decls     [][2]string
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// parseCSS reads St's CSS: rules of selectors and declarations, no nesting; @-rules are skipped.
func parseCSS(text string) []cssRule {
	text = cssComment.ReplaceAllString(text, "")
	var rules []cssRule
	for {
		open := strings.IndexByte(text, '{')
		if open < 0 {
			return rules
		}
		depth, end := 0, -1
		for i := open; i < len(text) && end < 0; i++ {
			switch text[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			return rules
		}
		sel, body := text[:open], text[open+1:end]
		text = text[end+1:]
		// A statement (@import ...;) before the selector is not part of it.
		if i := strings.LastIndexByte(sel, ';'); i >= 0 {
			sel = sel[i+1:]
		}
		sel = strings.TrimSpace(sel)
		if strings.HasPrefix(sel, "@") {
			continue
		}
		var r cssRule
		for _, s := range strings.Split(sel, ",") {
			r.selectors = append(r.selectors, strings.Join(strings.Fields(s), " "))
		}
		for _, d := range splitOutside(body, ';') {
			k, v, ok := strings.Cut(d, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
			r.decls = append(r.decls, [2]string{strings.ToLower(strings.TrimSpace(k)), v})
		}
		rules = append(rules, r)
	}
}

// splitOutside splits s at sep where it is not inside parentheses or quotes: url("a;b") stays one.
func splitOutside(s string, sep byte) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

var cssSimple = regexp.MustCompile(`[.#:]{0,2}[-\w]+`)

// subjectHas is whether a selector's subject - its last compound - is target, a compound of
// classes and ids, with at most a type selector besides: the rule styles that actor at rest. A
// pseudo class is another state, and another class or id on the same compound a variant
// (#panel.unlock-screen is the lock screen's bar).
func subjectHas(selector, target string) bool {
	f := strings.Fields(strings.NewReplacer(">", " ", "+", " ", "~", " ").Replace(selector))
	if len(f) == 0 {
		return false
	}
	last := f[len(f)-1]
	if strings.Contains(last, ":") {
		return false
	}
	want := cssSimple.FindAllString(target, -1)
	have := cssSimple.FindAllString(last, -1)
	for _, s := range have {
		if (strings.HasPrefix(s, ".") || strings.HasPrefix(s, "#")) && !slices.Contains(want, s) {
			return false
		}
	}
	for _, s := range want {
		if !slices.Contains(have, s) {
			return false
		}
	}
	return len(want) > 0
}

// cssSubject merges the declarations of every rule whose subject carries target, later over earlier.
func cssSubject(rules []cssRule, target string) (map[string]string, bool) {
	m := map[string]string{}
	found := false
	for _, r := range rules {
		for _, s := range r.selectors {
			if subjectHas(s, target) {
				found = true
				for _, d := range r.decls {
					m[d[0]] = d[1]
				}
				break
			}
		}
	}
	return m, found
}

var cssRGB = regexp.MustCompile(`rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+%?)\s*)?\)`)

// cssColors are the colour literals in a CSS value: hex, rgb() and rgba(), and the keywords
// transparent, black and white.
func cssColors(v string) []color.NRGBA {
	var out []color.NRGBA
	for _, m := range cssRGB.FindAllStringSubmatch(v, -1) {
		ch := func(s string) uint8 { f, _ := strconv.ParseFloat(s, 64); return uint8(math.Round(min(max(f, 0), 255))) }
		c := color.NRGBA{ch(m[1]), ch(m[2]), ch(m[3]), 255}
		if m[4] != "" {
			a, _ := strconv.ParseFloat(strings.TrimSuffix(m[4], "%"), 64)
			if strings.HasSuffix(m[4], "%") {
				a /= 100
			}
			c.A = uint8(math.Round(min(max(a, 0), 1) * 255))
		}
		out = append(out, c)
	}
	for _, w := range strings.FieldsFunc(cssRGB.ReplaceAllString(v, " "), func(r rune) bool { return r == ' ' || r == ',' || r == '(' || r == ')' }) {
		switch {
		case strings.HasPrefix(w, "#"):
			if c, ok := parseHex(w); ok {
				out = append(out, c)
			}
		case w == "transparent":
			out = append(out, color.NRGBA{})
		case w == "black":
			out = append(out, color.NRGBA{0, 0, 0, 255})
		case w == "white":
			out = append(out, color.NRGBA{255, 255, 255, 255})
		}
	}
	return out
}

// cssBackground is a declaration set's background colour: background-color's, else the background
// shorthand's.
func cssBackground(m map[string]string) (color.NRGBA, bool) {
	for _, k := range []string{"background-color", "background"} {
		if v, ok := m[k]; ok {
			if c := cssColors(v); len(c) > 0 {
				return c[0], true
			}
		}
	}
	return color.NRGBA{}, false
}

// cssRing is a declaration set's border: its width in px and its colour.
func cssRing(m map[string]string) (int, color.NRGBA, bool) {
	width, col, okC := -1, color.NRGBA{}, false
	parse := func(v string) {
		for _, f := range strings.Fields(cssRGB.ReplaceAllString(v, "")) {
			if n, err := strconv.Atoi(strings.TrimSuffix(f, "px")); err == nil && strings.HasSuffix(f, "px") {
				width = n
			}
			if f == "none" || f == "0" {
				width = 0
			}
		}
		if c := cssColors(v); len(c) > 0 {
			col, okC = c[0], true
		}
	}
	if v, ok := m["border"]; ok {
		parse(v)
	}
	if v, ok := m["border-width"]; ok {
		parse(v)
	}
	if v, ok := m["border-color"]; ok {
		parse(v)
	}
	return width, col, width >= 0 && okC
}

// The surfaces of the elevation table (THEMING.md), by the selector subject that styles each.
var (
	cssBars   = []string{"#panel"}
	cssLevel2 = []string{".popup-menu-content", ".dash-label", ".window-caption", ".search-entry", ".search-section-content",
		".notification-banner", "#dashtopanelPreviewScrollview"}
	cssLevel3 = []string{".modal-dialog", ".osd-window", ".app-folder-dialog"}
	// Rings without a background of their own: dash-to-panel paints the dock with #panel's, and
	// marks its panel .dock in dock mode.
	cssRingsOnly = []string{".dashtopanelPanel.dock"}
)

// cssContract is G1: the bars are g with the ring, levels 2 and 3 h with the ring, and nothing is
// the stock shell's black at 65%, which no level of the elevation table is. Each subject's rules are merged in order - the St-computed styles the views
// measure are the final word; this says which rule broke one.
func cssContract(rules []cssRule, text string, p lfPalette) (bool, string) {
	ring := p.colors["dark-border"]
	var bad []string
	checkOne := func(subject string, bg *color.NRGBA) {
		m, found := cssSubject(rules, subject)
		if !found {
			bad = append(bad, subject+": no rule")
			return
		}
		if bg != nil {
			if c, ok := cssBackground(m); !ok || c != (color.NRGBA{bg.R, bg.G, bg.B, 255}) {
				bad = append(bad, fmt.Sprintf("%s: background %q, want %s", subject, m["background-color"]+m["background"], hexOf(*bg)))
			}
		}
		if w, c, ok := cssRing(m); !ok || w != 1 || c != (color.NRGBA{ring.R, ring.G, ring.B, 255}) {
			bad = append(bad, fmt.Sprintf("%s: border %q, want 1px %s", subject, m["border"]+m["border-width"]+m["border-color"], hexOf(ring)))
		}
	}
	g, h := p.colors["neutral-g"], p.colors["neutral-h"]
	for _, s := range cssBars {
		checkOne(s, &g)
	}
	for _, s := range append(append([]string{}, cssLevel2...), cssLevel3...) {
		checkOne(s, &h)
	}
	for _, s := range cssRingsOnly {
		checkOne(s, nil)
	}
	if strings.Contains(strings.ReplaceAll(text, " ", ""), "rgba(0,0,0,0.65)") {
		bad = append(bad, "rgba(0, 0, 0, 0.65) is in it")
	}
	return len(bad) == 0, fmt.Sprintf("%d surfaces: %v", len(cssBars)+len(cssLevel2)+len(cssLevel3)+len(cssRingsOnly), bad)
}

// cssFocusExempt are the controls whose focus is not the ring: a popup-menu item's hover is its
// focus, and a calendar day's ring means today.
var cssFocusExempt = []string{"popup-menu-item", "calendar-day"}

// cssFocus is G2: no :focus rule takes the box-shadow away from a control that is not exempt, and
// focus is drawn as the inset 2 px accent ring. A none that a later rule draws the ring over again,
// on the same selector or on a part inside it (the clock's label in the clock's button), is how a
// theme resets a shared hover rule or moves the ring, not a missing one.
func cssFocus(rules []cssRule, accent color.NRGBA) (bool, string) {
	none := map[string]bool{}
	var order []string
	rings := 0
	for _, r := range rules {
		var focus []string
		for _, s := range r.selectors {
			if strings.Contains(s, ":focus") {
				focus = append(focus, s)
			}
		}
		if len(focus) == 0 {
			continue
		}
		for _, d := range r.decls {
			if d[0] != "box-shadow" {
				continue
			}
			if d[1] == "none" {
				for _, s := range focus {
					exempt := false
					for _, e := range cssFocusExempt {
						exempt = exempt || strings.Contains(s, e)
					}
					if !exempt && !none[s] {
						none[s] = true
						order = append(order, s)
					}
				}
				continue
			}
			if c := cssColors(d[1]); strings.HasPrefix(strings.Join(strings.Fields(d[1]), " "), "inset 0 0 0 2px") &&
				len(c) == 1 && c[0] == (color.NRGBA{accent.R, accent.G, accent.B, 255}) {
				rings++
				for _, s := range focus {
					for n := range none {
						if s == n || strings.HasPrefix(s, n+" ") {
							delete(none, n)
						}
					}
				}
			}
		}
	}
	var left []string
	for _, s := range order {
		if none[s] {
			left = append(left, s)
		}
	}
	return len(left) == 0 && rings > 0, fmt.Sprintf("%d rules draw the inset 2px %s ring; box-shadow none on focus: %v", rings, hexOf(accent), left)
}

// cssPalette is G3: every colour a stylesheet writes is the palette's, or white or black, at any
// alpha - a mix() or darken() result is neither, and an opaque grey least of all.
func cssPalette(rules []cssRule, p lfPalette) (bool, string) {
	off := map[string]int{}
	greys := 0
	for _, r := range rules {
		for _, d := range r.decls {
			for _, c := range cssColors(d[1]) {
				if c.A == 0 || p.has(c) || (c.R == c.G && c.G == c.B && (c.R == 0 || c.R == 255)) {
					continue
				}
				k := hexOf(c)
				if c.R == c.G && c.G == c.B {
					k += " (grey)"
					greys++
				}
				off[k]++
			}
		}
	}
	keys := make([]string, 0, len(off))
	for k := range off {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return off[keys[i]] > off[keys[j]] || off[keys[i]] == off[keys[j]] && keys[i] < keys[j]
	})
	shown := keys
	if len(shown) > 12 {
		shown = shown[:12]
	}
	var b strings.Builder
	for _, k := range shown {
		fmt.Fprintf(&b, " %s x%d", k, off[k])
	}
	return len(off) == 0, fmt.Sprintf("%d colours off the palette (%d greys):%s", len(off), greys, b.String())
}

// cssWeights is G6: the shell's text is regular or bold, nothing between or beyond.
func cssWeights(rules []cssRule) (bool, string) {
	var bad []string
	for _, r := range rules {
		for _, d := range r.decls {
			if d[0] != "font-weight" {
				continue
			}
			switch d[1] {
			case "normal", "bold", "400", "700", "inherit":
			default:
				bad = append(bad, strings.Join(r.selectors, ", ")+": "+d[1])
			}
		}
	}
	return len(bad) == 0, fmt.Sprintf("weights other than 400 and 700: %v", bad)
}

// The boot film: the screen from the firmware to the shell's first frame, every filmInterval.
const (
	filmInterval = 100 * time.Millisecond
	// A frame is Plymouth's when the mark's cyan is on it and the rest is the boot's black.
	filmMarkPixels = 400
	filmBlackShare = 0.85
	// No wrong ground may cover more than this share of a frame once Plymouth has started.
	filmMaxCover = 0.40
	// systemd-boot's selected entry is a bar of the firmware console's grey at least this long.
	filmMenuRun = 120
)

// filmGrounds are what must not cover the screen once Plymouth has drawn: GNOME's
// SystemBackground (#282828, which recipes/gnome-shell patches to black), GTK's dark window
// (#222226), and f (#11181E), a window's body, which the overview and lock grounds are not.
var filmGrounds = []color.NRGBA{{0x28, 0x28, 0x28, 255}, {0x22, 0x22, 0x26, 255}, {0x11, 0x18, 0x1E, 255}}

// filmFrame is what one frame of the boot showed.
type filmFrame struct {
	at       time.Duration
	cyan     int
	mark     lfRect
	black    float64
	menu     bool
	cover    float64
	coverHex string
}

// markHue is the mark's lines, cyan or teal: bright (the wordmark's faint cyan glow is not), its red
// well under its green and blue (the wordmark is white), and its green near its blue (the
// firmware's logo is blue).
func markHue(r, g, b uint8) bool {
	return min(g, b) > markLineLevel && int(r)*2 < int(min(g, b)) && int(g)*4 >= int(b)*3
}

// markLineLevel is how bright the mark's lines are in green and blue at the least: the overview's
// teal (#0097A7) is above it, the glow around the wordmark under it.
const markLineLevel = 0x80

func (f filmFrame) plymouth() bool { return f.cyan >= filmMarkPixels && f.black >= filmBlackShare }

// filmStats reads a frame in one pass: the mark's cyan and its extents, the black, systemd-boot's
// menu bar, and the largest share a wrong ground covers.
func filmStats(img *image.RGBA) filmFrame {
	b := img.Bounds()
	var f filmFrame
	box := image.Rectangle{}
	black := 0
	cover := make([]int, len(filmGrounds))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		run, runC := 0, [3]uint8{}
		for x := b.Min.X; x < b.Max.X; x++ {
			i := img.PixOffset(x, y)
			r, g, bl := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
			if max(r, g, bl) <= 2 {
				black++
			}
			if markHue(r, g, bl) {
				f.cyan++
				p := image.Rect(x, y, x+1, y+1)
				if box.Empty() {
					box = p
				} else {
					box = box.Union(p)
				}
			}
			for k, c := range filmGrounds {
				if channelDelta(color.NRGBA{r, g, bl, 255}, c) <= lfSurfaceTol {
					cover[k]++
				}
			}
			grey := r >= 0x60 && r <= 0xE0 && abs(int(r)-int(g)) <= 2 && abs(int(g)-int(bl)) <= 2
			if grey && run > 0 && runC == [3]uint8{r, g, bl} {
				run++
			} else if grey {
				run, runC = 1, [3]uint8{r, g, bl}
			} else {
				run = 0
			}
			if run >= filmMenuRun {
				f.menu = true
			}
		}
	}
	n := float64(b.Dx() * b.Dy())
	f.black = float64(black) / n
	f.mark = lfRect{box.Min.X, box.Min.Y, box.Dx(), box.Dy()}
	for k, c := range cover {
		if s := float64(c) / n; s > f.cover {
			f.cover, f.coverHex = s, hexOf(filmGrounds[k])
		}
	}
	return f
}

// film records the boot while the harness waits for it.
type film struct {
	stopc  chan struct{}
	done   chan struct{}
	frames []filmFrame
	failed int
}

// startFilm screenshots v every filmInterval into dir and reads each frame a tick after asking for
// it, so QEMU has written it; the frames worth looking at are kept as PNGs when it stops.
func startFilm(v *vm, dir string) *film {
	f := &film{stopc: make(chan struct{}), done: make(chan struct{})}
	_ = os.MkdirAll(dir, 0o755)
	go func() {
		defer close(f.done)
		start := time.Now()
		keep := map[string]*image.RGBA{}
		var worst float64
		pending, pendingAt := "", time.Duration(0)
		read := func() {
			if pending == "" {
				return
			}
			img, err := decodePPM(pending)
			_ = os.Remove(pending)
			pending = ""
			if err != nil {
				f.failed++
				return
			}
			s := filmStats(img)
			s.at = pendingAt
			f.frames = append(f.frames, s)
			started := false
			for _, x := range f.frames {
				started = started || x.plymouth()
			}
			switch {
			case s.menu && keep["menu"] == nil:
				keep["menu"] = img
			case s.plymouth() && keep["plymouth"] == nil:
				keep["plymouth"] = img
			}
			if started && s.cover > worst {
				worst, keep["worst-ground"] = s.cover, img
			}
			keep["last"] = img
		}
		t := time.NewTicker(filmInterval)
		defer t.Stop()
		for n := 0; ; n++ {
			select {
			case <-f.stopc:
				read()
				for name, img := range keep {
					_ = writePNG(filepath.Join(dir, name+".png"), img)
				}
				return
			case <-t.C:
			}
			read()
			p := filepath.Join(dir, fmt.Sprintf("%05d.ppm", n))
			if v.screenshot(p) == nil {
				pending, pendingAt = p, time.Since(start)
			}
		}
	}()
	return f
}

func (f *film) stop() []filmFrame {
	close(f.stopc)
	<-f.done
	return f.frames
}

// filmReport checks the boot's film: once Plymouth has drawn, no wrong ground covers the screen;
// how long systemd-boot's menu showed; and where Plymouth put the mark, for lf.finish to hold
// against the overview's card.
func (r *report) filmReport(frames []filmFrame, failed int) *[2]float64 {
	first := -1
	for i, f := range frames {
		if f.plymouth() {
			first = i
			break
		}
	}
	if first < 0 {
		r.add("boot film", false, fmt.Sprintf("%d frames (%d unreadable), none of Plymouth's mark on black: see film/", len(frames), failed))
		return nil
	}
	var worst filmFrame
	for _, f := range frames[first:] {
		if f.cover > worst.cover {
			worst = f
		}
	}
	r.add("boot film: ground", worst.cover <= filmMaxCover,
		fmt.Sprintf("%d frames from Plymouth at %.1fs; the most a wrong ground covered: %s %.0f%% at %.1fs, want <= %.0f%% (film/worst-ground.png)",
			len(frames)-first, frames[first].at.Seconds(), worst.coverHex, worst.cover*100, worst.at.Seconds(), filmMaxCover*100))
	var menuFrom, menuTo time.Duration = -1, -1
	for _, f := range frames[:first] {
		if f.menu {
			if menuFrom < 0 {
				menuFrom = f.at
			}
			menuTo = f.at
		}
	}
	if menuFrom >= 0 {
		r.add("boot film: systemd-boot menu", true, fmt.Sprintf("on screen %.1fs, from %.1fs", (menuTo-menuFrom+filmInterval).Seconds(), menuFrom.Seconds()))
	} else {
		r.add("boot film: systemd-boot menu", true, "never on screen")
	}
	// The mark once it is whole: the intro draws it piece by piece.
	largest := 0
	for _, f := range frames[first:] {
		if f.plymouth() {
			largest = max(largest, f.mark.W*f.mark.H)
		}
	}
	var xs, ys []float64
	for _, f := range frames[first:] {
		if f.plymouth() && f.mark.W*f.mark.H*10 >= largest*9 {
			xs = append(xs, float64(f.mark.X)+float64(f.mark.W)/2)
			ys = append(ys, float64(f.mark.Y)+float64(f.mark.H)/2)
		}
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	return &[2]float64{xs[len(xs)/2], ys[len(ys)/2]}
}

// The rows Plymouth's prompt is looked for in, at 1280x800: below the mark and its wordmark, around
// the pill's middle at 612.
const promptFrom, promptTo = 560, 680

// promptLook checks Plymouth's password prompt as test:lifecycle shows it: a pill on g with an
// accent ring, centred under the mark on the boot's black. The ring is looked for down the middle
// column.
func promptLook(img image.Image, accent, fill, ground color.NRGBA) (bool, string) {
	cx := img.Bounds().Dx() / 2
	top := -1
	for y := promptFrom; y < min(promptTo, img.Bounds().Dy()); y++ {
		if near(at(img, cx, y), accent, 8) {
			top = y
			break
		}
	}
	if top < 0 {
		return false, fmt.Sprintf("no %s ring at x %d from y %d to %d", hexOf(accent), cx, promptFrom, promptTo)
	}
	inside, _ := modeColor(img, lfRect{cx - 80, top + 6, 160, 24})
	corner := at(img, cx-160, top)
	ok := near(inside, fill, lfTextTol) && near(corner, ground, lfTextTol)
	return ok, fmt.Sprintf("ring at y %d; inside %s (want %s), the pill's corner %s (want %s)", top, hexOf(inside), hexOf(fill), hexOf(corner), hexOf(ground))
}

// plymouthPrompt checks test:lifecycle's password prompt (password.png) and the screen 1.5 s after
// the password (password-after.png), in the palette of the system the disk then booted.
func (r *report) plymouthPrompt(g *guest, dir string) {
	raw, err := g.run("cat /"+brandPalette, "")
	if err != nil {
		r.add("lf: prompt plymouth", false, "/"+brandPalette+" is not on the guest"+errText(err))
		return
	}
	p, err := parseLFPalette([]byte(raw))
	if err != nil {
		r.add("lf: prompt plymouth", false, err.Error())
		return
	}
	prompt, err := readPNG(filepath.Join(dir, "password.png"))
	if err != nil {
		r.add("lf: prompt plymouth", false, err.Error())
		return
	}
	ok, detail := promptLook(prompt, p.colors["dark-accent"], p.colors["neutral-g"], p.colors["brand-boot"])
	r.add("lf: prompt plymouth", ok, detail+" (password.png)")
	// Any of the screens after the password showing the grid back is the outro going back.
	afters, _ := filepath.Glob(filepath.Join(dir, "password-after-*.png"))
	if len(afters) == 0 {
		r.add("lf: grid plymouth", false, "no screens after the password")
		return
	}
	sort.Strings(afters)
	for _, f := range afters {
		after, err := readPNG(f)
		if err != nil {
			continue
		}
		if ok, detail = gridBack(prompt, after, promptFrom); ok {
			break
		}
		detail += " (" + filepath.Base(f) + ")"
	}
	r.add("lf: grid plymouth", ok, detail)
}

// gridBack is whether the grid came back after the password: above the prompt there is at least
// this much more of the mark's cyan than the outro left with the prompt.
func gridBack(prompt, after image.Image, promptTop int) (bool, string) {
	const more = 1.25
	count := func(img image.Image) int {
		n := 0
		for y := 0; y < promptTop-20; y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				c := at(img, x, y)
				if markHue(c.R, c.G, c.B) {
					n++
				}
			}
		}
		return n
	}
	a, b := count(prompt), count(after)
	return float64(b) >= more*float64(a) && a > 0, fmt.Sprintf("%d px of the mark with the prompt, %d after, want %.2fx", a, b, more)
}
