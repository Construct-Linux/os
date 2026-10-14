// Package needle compares a view's screenshot with its needle - the screenshot of it someone
// approved - and reads and writes the spec beside each: what the view is, the screen it was drawn
// on, and the regions left out of the comparison.
package needle

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Construct-Linux/os/internal/pixels"
)

// A needle is how a view must look: the screenshot the harness took of it once someone looked at
// it and approved (github.com/Construct-Linux/needles, openQA's word for the same thing). Every
// run compares its screenshots with them; a view that changed beyond the tolerance fails, with
// the difference drawn; task needles:propose opens a pull request with it, and merging that is
// approving the new look.
const (
	// A pixel differs when a channel moved more than this: antialiasing and the GPU-less
	// renderer's rounding stay under it, a colour the theme changed does not.
	ChannelTolerance = 24
	// A view fails when more of its compared area than this differs: a few glyphs that moved
	// fail it, noise does not.
	MaxShare = 0.002
)

// Rect is a region of a view left out of the comparison, and why.
type Rect struct {
	X   int    `json:"x"`
	Y   int    `json:"y"`
	W   int    `json:"w"`
	H   int    `json:"h"`
	Why string `json:"why"`
}

// Spec is a view's <name>.json beside its <name>.png, in a run and in the needles, where the
// gallery reads it: every field is required, so a view says what it is without the harness's code.
//
// Group is the part of the harness that shoots the view (Groups); About, in a sentence, what
// it validates, and Rules by THEMING.md's ids; Scheme the colour scheme in force when it was shot;
// Screen the panel and the stage it was drawn on. Ignore is written by hand: regions a view's own
// content changes in. Actors is measured when the view is shot: where the clock, Vitals' readings,
// notifications' times and the lock clock were, which move with their text's width, so no fixed
// rectangle holds them; a comparison leaves out the needle's and the run's both, so text that grew
// or shrank between them is left out where it was in either. LF is what lf's checks said of the
// view in the run. Approved is the needle's alone: which build and commit made it, and how far it
// was from the needle it replaced.
type Spec struct {
	Group    string    `json:"group"`
	About    string    `json:"about"`
	Rules    []string  `json:"rules"`
	Scheme   string    `json:"scheme"`
	Screen   Screen    `json:"screen"`
	Ignore   []Rect    `json:"ignore"`
	Actors   []Rect    `json:"actors"`
	Approved *Approval `json:"approved,omitempty"`
	LF       LF        `json:"lf"`
	Noise    Noise     `json:"noise"`
}

// Noise is how busy a view is, measured on its screenshot: the share of its pixels on an edge
// (a neighbour, right or below, beyond ChannelTolerance), and how many of the palette's colours
// cover a surface of it. Informative, never a gate: it is for a reviewer to see a view grow
// noisier from one approval to the next, which no single rule says.
type Noise struct {
	Edges         float64 `json:"edges"`
	PaletteColors int     `json:"palette_colors"`
}

// noisePaletteShare is the least of a view a palette colour covers to count: less is antialiasing
// or a mark's edge, not a surface the eye takes as one of the view's colours (at 2x, 0.1% of the
// screen is a 72 px square).
const noisePaletteShare = 0.001

// NoiseOf measures img's noise against the palette's colours, each within tol.
func NoiseOf(img image.Image, palette []color.NRGBA, tol int) Noise {
	b := img.Bounds()
	n := image.NewNRGBA(b)
	draw.Draw(n, b, img, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return Noise{}
	}
	// A colour is its RGB alone, as pixels.ChannelDelta compares it: every pixel is counted opaque.
	px := func(x, y int) color.NRGBA {
		i := y*n.Stride + x*4
		return color.NRGBA{n.Pix[i], n.Pix[i+1], n.Pix[i+2], 255}
	}
	edges := 0
	hist := map[color.NRGBA]int{}
	for y := range h {
		for x := range w {
			p := px(x, y)
			hist[p]++
			if (x+1 < w && pixels.ChannelDelta(p, px(x+1, y)) > ChannelTolerance) || (y+1 < h && pixels.ChannelDelta(p, px(x, y+1)) > ChannelTolerance) {
				edges++
			}
		}
	}
	out := Noise{Edges: float64(edges) / float64(w*h)}
	seen := map[color.NRGBA]bool{}
	for _, c := range palette {
		rgb := color.NRGBA{c.R, c.G, c.B, 255}
		if seen[rgb] {
			continue
		}
		seen[rgb] = true
		covered := 0
		for k, v := range hist {
			if pixels.ChannelDelta(k, rgb) <= tol {
				covered += v
			}
		}
		if float64(covered) >= noisePaletteShare*float64(w*h) {
			out.PaletteColors++
		}
	}
	return out
}

// Screen is the screen a view was drawn on: the panel in physical pixels, the stage in
// logical ones, and the scale between them - 2 for the X9-15 at 200%, 1.5 for desktop-scale-150.
type Screen struct {
	W      int     `json:"w"`
	H      int     `json:"h"`
	Scale  float64 `json:"scale"`
	StageW int     `json:"stage_w"`
	StageH int     `json:"stage_h"`
}

// Approval is how a needle came to be: the build that drew it, the os commit that build was
// made from, and the share of the needle it replaced that differed - none when it was new.
type Approval struct {
	Build string   `json:"build"`
	OS    string   `json:"os"`
	Share *float64 `json:"share"`
	New   bool     `json:"new"`
}

// LF is lf's checks on one view: how many passed, how many failed, and the failed ones' names.
// None at all is a view lf does not measure, which only a person's review covers.
type LF struct {
	Pass   int      `json:"pass"`
	Fail   int      `json:"fail"`
	Checks []string `json:"checks"`
}

// The groups a view is shot in, each by its own part of the harness: the shell's views, the
// desktop over each wallpaper, libadwaita's own surfaces, the apps as they open, Settings' panels,
// the type specimen and the installer.
const (
	GroupShell     = "shell"
	GroupDesktop   = "desktop"
	GroupGTK       = "gtk"
	GroupApp       = "app"
	GroupSettings  = "settings"
	GroupType      = "type"
	GroupInstaller = "installer"
)

var Groups = []string{GroupShell, GroupDesktop, GroupGTK, GroupApp, GroupSettings, GroupType, GroupInstaller}

// themingRule is a THEMING.md rule's id: a rule a view names must be one there is.
var themingRule = regexp.MustCompile(`^(C|P|S|E|F|R|SP|T|TY|X|W)[0-9]+$`)

// Validate says what a spec lacks; a needle must also say how it was approved.
func (s Spec) Validate(needle bool) error {
	var bad []string
	if !slices.Contains(Groups, s.Group) {
		bad = append(bad, fmt.Sprintf("group %q is none of %v", s.Group, Groups))
	}
	if strings.TrimSpace(s.About) == "" {
		bad = append(bad, "no about")
	}
	if len(s.Rules) == 0 {
		bad = append(bad, "no rules")
	}
	for _, r := range s.Rules {
		if !themingRule.MatchString(r) {
			bad = append(bad, fmt.Sprintf("rule %q is no THEMING.md id", r))
		}
	}
	if s.Scheme != "dark" && s.Scheme != "light" {
		bad = append(bad, fmt.Sprintf("scheme %q, want dark or light", s.Scheme))
	}
	if c := s.Screen; c.W <= 0 || c.H <= 0 || c.Scale <= 0 || c.StageW <= 0 || c.StageH <= 0 {
		bad = append(bad, fmt.Sprintf("screen %+v", c))
	}
	if needle {
		switch a := s.Approved; {
		case a == nil:
			bad = append(bad, "not approved")
		case a.Build == "" || a.OS == "":
			bad = append(bad, fmt.Sprintf("approved by build %q at os %q", a.Build, a.OS))
		case (a.Share == nil) != a.New:
			bad = append(bad, "approved: a share is what a view that was not new differed by")
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

// Write saves the spec as JSON at p, its lists as lists even when empty: the gallery reads them.
func (s Spec) Write(p string) error {
	s.Rules = append([]string{}, s.Rules...)
	s.Ignore = append([]Rect{}, s.Ignore...)
	s.Actors = append([]Rect{}, s.Actors...)
	s.LF.Checks = append([]string{}, s.LF.Checks...)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// BarFallback is what is left out of a view whose actors could not be measured, in a
// screenshot width px wide at scale: the top bar's inside from the middle on, its ring still
// compared. The bar is 6 px from the top and 36 high, in the stage's pixels.
func BarFallback(width, scale int) Rect {
	scale = max(scale, 1)
	w := width / scale
	return Rect{X: w / 2 * scale, Y: 8 * scale, W: (w/2 - 6) * scale, H: 32 * scale, Why: "the top bar's readings, not measured"}
}

// Stage is what the harness measures in the running shell as it shoots a view: the stage's
// size, the colour scheme and the actors whose content changes from run to run.
type Stage struct {
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Scheme string  `json:"scheme"`
	Actors []Rect  `json:"actors"`
}

// ScreenOf is the screen a screenshot w x h px was drawn on, over the stage seen; a stage not
// measured is taken as the panel over the run's scale.
func ScreenOf(w, h int, seen Stage, scale int) Screen {
	sw, sh := int(math.Round(seen.W)), int(math.Round(seen.H))
	if sw <= 0 || sh <= 0 {
		scale = max(scale, 1)
		sw, sh = w/scale, h/scale
	}
	k := 0.0
	if sw > 0 {
		// To the hundredth: mutter's nearest supported scale to 1.5 lays out a stage a pixel off.
		k = math.Round(float64(w)/float64(sw)*100) / 100
	}
	return Screen{W: w, H: h, Scale: k, StageW: sw, StageH: sh}
}

// OnScreen is a stage's rectangles in a screenshot width px wide: scaled by the screenshot over
// the stage, outward to whole pixels so a scaled rectangle still covers what it held.
func (s Stage) OnScreen(width int) []Rect {
	if s.W <= 0 || width <= 0 || math.Abs(float64(width)-s.W) < 0.5 {
		return s.Actors
	}
	k := float64(width) / s.W
	out := make([]Rect, 0, len(s.Actors))
	for _, r := range s.Actors {
		x0, y0 := math.Floor(float64(r.X)*k), math.Floor(float64(r.Y)*k)
		x1, y1 := math.Ceil(float64(r.X+r.W)*k), math.Ceil(float64(r.Y+r.H)*k)
		out = append(out, Rect{X: int(x0), Y: int(y0), W: int(x1 - x0), H: int(y1 - y0), Why: r.Why})
	}
	return out
}

// Sorted is rs in one order, a copy: by place, then size, then why.
func Sorted(rs []Rect) []Rect {
	out := slices.Clone(rs)
	slices.SortFunc(out, func(a, b Rect) int {
		return cmp.Or(cmp.Compare(a.X, b.X), cmp.Compare(a.Y, b.Y), cmp.Compare(a.W, b.W), cmp.Compare(a.H, b.H), cmp.Compare(a.Why, b.Why))
	})
	return out
}

// Union is where either measurement found an actor, one region an actor: the same actor's
// rectangles that overlap - the same place twice, or a clock that moved a few pixels between the
// two measurements - become the one rectangle that holds them both. Two regions for one actor
// made a needle's exclusion read as two things, and the gallery drew both.
func Union(a, b []Rect) []Rect {
	var out []Rect
	for _, r := range Sorted(slices.Concat(a, b)) {
		i := slices.IndexFunc(out, func(o Rect) bool { return o.Why == r.Why && overlaps(o, r) })
		if i < 0 {
			out = append(out, r)
			continue
		}
		o := out[i]
		x0, y0 := min(o.X, r.X), min(o.Y, r.Y)
		x1, y1 := max(o.X+o.W, r.X+r.W), max(o.Y+o.H, r.Y+r.H)
		out[i] = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Why: o.Why}
	}
	return Sorted(out)
}

// overlaps says whether two rectangles share any pixel.
func overlaps(a, b Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// SpecError is a needle whose .json is missing or not as the harness writes it: nothing of
// the view can be compared with it.
type SpecError struct{ err error }

func (e SpecError) Error() string { return e.err.Error() }
func (e SpecError) Unwrap() error { return e.err }

// ReadSpec reads the Spec at p and validates it, as a needle's when needle is set: every
// view has one, in a run and in the needles.
func ReadSpec(p string, needle bool) (Spec, error) {
	var spec Spec
	raw, err := os.ReadFile(p)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	if err := spec.Validate(needle); err != nil {
		return spec, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return spec, nil
}

// Compare is the share of ref's pixels, outside the ignored regions, that differ in shot;
// it draws them red over ref dimmed into diff. What is ignored is the needle's spec (specPath) and
// the actors measured with the shot (its own .json beside it).
func Compare(ref, shot, specPath, diff string) (float64, error) {
	a, err := pixels.ReadPNG(ref)
	if err != nil {
		return 0, err
	}
	b, err := pixels.ReadPNG(shot)
	if err != nil {
		return 0, err
	}
	if a.Bounds() != b.Bounds() {
		return 1, fmt.Errorf("%v, the needle %v: the screen size changed", b.Bounds().Size(), a.Bounds().Size())
	}
	// A needle that cannot say what it is, or was drawn on another screen, is no needle for this
	// view: all of it differs.
	spec, err := ReadSpec(specPath, true)
	if err != nil {
		return 1, SpecError{err}
	}
	run, err := ReadSpec(strings.TrimSuffix(shot, ".png")+".json", false)
	if err != nil {
		return 1, err
	}
	if spec.Screen != run.Screen {
		return 1, fmt.Errorf("drawn on %+v, the needle on %+v: the screen changed", run.Screen, spec.Screen)
	}
	ignore := slices.Concat(spec.Ignore, spec.Actors, run.Actors)
	rb := a.Bounds()
	out := image.NewNRGBA(rb)
	compared, differ := 0, 0
	for y := rb.Min.Y; y < rb.Max.Y; y++ {
		for x := rb.Min.X; x < rb.Max.X; x++ {
			ca := pixels.At(a, x, y)
			skipped := false
			for _, r := range ignore {
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					skipped = true
					break
				}
			}
			// The reference, dimmed: blue where ignored, red where it differs.
			dim := color.NRGBA{ca.R / 3, ca.G / 3, ca.B / 3, 255}
			if skipped {
				dim.B = 90
				out.SetNRGBA(x, y, dim)
				continue
			}
			compared++
			cb := pixels.At(b, x, y)
			if pixels.ChannelDelta(ca, cb) > ChannelTolerance {
				differ++
				dim = color.NRGBA{255, 40, 40, 255}
			}
			out.SetNRGBA(x, y, dim)
		}
	}
	if err := pixels.WritePNG(diff, out); err != nil {
		return 0, err
	}
	if compared == 0 {
		return 0, errors.New("everything is ignored")
	}
	return float64(differ) / float64(compared), nil
}
