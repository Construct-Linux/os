package needle

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/pixels"
)

var (
	tG      = color.NRGBA{0x17, 0x20, 0x28, 255}
	tRing   = color.NRGBA{0x2A, 0x38, 0x43, 255}
	tAccent = color.NRGBA{0x00, 0xE5, 0xFF, 255}
	// tPalette has g twice, as a palette does under two tokens.
	tPalette = []color.NRGBA{tG, tG, tRing, tAccent, {0x0B, 0x11, 0x16, 255}}
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

func fill(img *image.NRGBA, r pixels.Rect, c color.NRGBA) {
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// tScreen is a 100x100 test view's screen.
var tScreen = Screen{W: 100, H: 100, Scale: 1, StageW: 100, StageH: 100}

// tSpec is a whole spec for a test view; a needle's is approved.
func tSpec(approved bool, ignore, actors []Rect) Spec {
	s := Spec{Group: GroupShell, About: "a test view", Rules: []string{"S1"}, Scheme: "dark", Screen: tScreen, Ignore: ignore, Actors: actors}
	if approved {
		share := 0.01
		s.Approved = &Approval{Build: "20261004.1", OS: "abc", Share: &share}
	}
	return s
}

func writeSpec(t *testing.T, p string, s Spec) {
	t.Helper()
	if err := s.Write(p); err != nil {
		t.Fatal(err)
	}
}

// Noise is the share of pixels on an edge and the palette colours that cover a surface, each
// within the tolerance; a colour the palette has twice counts once, a speck of one not at all.
func TestNoiseOf(t *testing.T) {
	img := solid(100, 100, tG)
	fill(img, pixels.Rect{X: 50, Y: 0, W: 50, H: 100}, tAccent)
	img.SetNRGBA(10, 10, tRing)
	n := NoiseOf(img, tPalette, 2)
	// The boundary's column, and the speck's left, top and own pixel.
	if n.Edges != 103.0/10000 || n.PaletteColors != 2 {
		t.Errorf("noise %+v", n)
	}
	if n := NoiseOf(img, nil, 2); n.PaletteColors != 0 || n.Edges == 0 {
		t.Errorf("without a palette %+v", n)
	}
}

// A view differs by the share of its compared pixels a theme change moved, not by noise, and
// leaves out what its .json ignores.
func TestCompareNeedle(t *testing.T) {
	dir := t.TempDir()
	ink := color.NRGBA{0x0B, 0x11, 0x16, 255}
	ref := filepath.Join(dir, "ref.png")
	if err := pixels.WritePNG(ref, solid(100, 100, ink)); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(dir, "ref.json")
	writeSpec(t, spec, tSpec(true, nil, nil))
	shot := func(name string, img image.Image) string {
		p := filepath.Join(dir, name+".png")
		if err := pixels.WritePNG(p, img); err != nil {
			t.Fatal(err)
		}
		writeSpec(t, filepath.Join(dir, name+".json"), tSpec(false, nil, nil))
		return p
	}
	diff := filepath.Join(dir, "diff.png")

	// Noise under the channel tolerance: nothing differs.
	noisy := solid(100, 100, color.NRGBA{0x0B + 10, 0x11, 0x16, 255})
	if share, err := Compare(ref, shot("noisy", noisy), spec, diff); err != nil || share != 0 {
		t.Errorf("noise: %v, %v", share, err)
	}

	// A 10x10 square of another colour: 100 of the compared pixels.
	changed := solid(100, 100, ink)
	for y := 50; y < 60; y++ {
		for x := 10; x < 20; x++ {
			changed.SetNRGBA(x, y, color.NRGBA{0x1F, 0x2A, 0x33, 255})
		}
	}
	share, err := Compare(ref, shot("changed", changed), spec, diff)
	if err != nil {
		t.Fatal(err)
	}
	if want := 100.0 / 10000; share != want {
		t.Errorf("share = %v, want %v", share, want)
	}

	// The same square ignored by the needle's .json: nothing differs.
	ignoring := filepath.Join(dir, "ignoring.json")
	writeSpec(t, ignoring, tSpec(true, []Rect{{X: 10, Y: 50, W: 10, H: 10, Why: "a clock face"}}, nil))
	if share, err := Compare(ref, shot("changed", changed), ignoring, diff); err != nil || share != 0 {
		t.Errorf("ignored: %v, %v", share, err)
	}

	// A needle drawn on another screen, or one that does not say what it is, is no needle for it.
	other := tSpec(true, nil, nil)
	other.Screen.Scale, other.Screen.StageW, other.Screen.StageH = 2, 50, 50
	writeSpec(t, filepath.Join(dir, "other.json"), other)
	if share, err := Compare(ref, shot("noisy", noisy), filepath.Join(dir, "other.json"), diff); err == nil || share != 1 {
		t.Errorf("another screen: %v, %v", share, err)
	}
	unapproved := tSpec(false, nil, nil)
	writeSpec(t, filepath.Join(dir, "unapproved.json"), unapproved)
	if _, err := Compare(ref, shot("noisy", noisy), filepath.Join(dir, "unapproved.json"), diff); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("a needle not approved: %v", err)
	}

	// A clock whose text changed width: the needle drew it at x 10-20, this run at x 30-45. Each
	// side's measured actors leave out where it was there, so neither place differs.
	clockRef := solid(100, 100, ink)
	fill(clockRef, pixels.Rect{X: 10, Y: 80, W: 10, H: 8}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	clockRefPath := filepath.Join(dir, "clock-ref.png")
	if err := pixels.WritePNG(clockRefPath, clockRef); err != nil {
		t.Fatal(err)
	}
	clockSpec := filepath.Join(dir, "clock-ref.json")
	writeSpec(t, clockSpec, tSpec(true, nil, []Rect{{X: 8, Y: 78, W: 14, H: 12, Why: "the clock"}}))
	moved := solid(100, 100, ink)
	fill(moved, pixels.Rect{X: 30, Y: 80, W: 15, H: 8}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	movedPath := shot("moved", moved)
	if share, err := Compare(clockRefPath, movedPath, clockSpec, diff); err != nil || share == 0 {
		t.Errorf("the run's clock, unmeasured: %v, %v", share, err)
	}
	writeSpec(t, filepath.Join(dir, "moved.json"), tSpec(false, nil, []Rect{{X: 28, Y: 78, W: 19, H: 12, Why: "the clock"}}))
	if share, err := Compare(clockRefPath, movedPath, clockSpec, diff); err != nil || share != 0 {
		t.Errorf("the clock, measured in both: %v, %v", share, err)
	}

	// Another size is a failure of its own.
	if _, err := Compare(ref, shot("small", solid(50, 50, ink)), spec, diff); err == nil {
		t.Error("a different size: no error")
	}
}

// A spec says what its view is, every field of it; a needle also how it was approved, and a share
// only when it replaced one.
func TestSpecValidate(t *testing.T) {
	if err := tSpec(false, nil, nil).Validate(false); err != nil {
		t.Errorf("a whole run spec: %v", err)
	}
	if err := tSpec(true, nil, nil).Validate(true); err != nil {
		t.Errorf("a whole needle: %v", err)
	}
	bad := tSpec(true, nil, nil)
	bad.Group, bad.About, bad.Rules, bad.Scheme, bad.Screen = "misc", "", []string{"E2", "lf"}, "auto", Screen{}
	bad.Approved.Share = nil
	err := bad.Validate(true)
	for _, want := range []string{`group "misc"`, "no about", `rule "lf"`, `scheme "auto"`, "screen", "approved: a share"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q not said: %v", want, err)
		}
	}
	// Written, every list is a list, empty or not.
	p := filepath.Join(t.TempDir(), "v.json")
	writeSpec(t, p, tSpec(false, nil, nil))
	raw, _ := os.ReadFile(p)
	for _, want := range []string{`"ignore": []`, `"actors": []`, `"checks": []`, `"stage_h": 100`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
}

// The screen is the panel over the stage measured, the scale to the hundredth.
func TestScreenOf(t *testing.T) {
	if s := ScreenOf(2880, 1800, Stage{W: 1440, H: 900}, 2); s != (Screen{2880, 1800, 2, 1440, 900}) {
		t.Errorf("200%%: %+v", s)
	}
	if s := ScreenOf(2880, 1800, Stage{W: 1921, H: 1201}, 2); s.Scale != 1.5 {
		t.Errorf("150%%, a stage a pixel off: %+v", s)
	}
	if s := ScreenOf(2880, 1800, Stage{}, 2); s != (Screen{2880, 1800, 2, 1440, 900}) {
		t.Errorf("unmeasured: %+v", s)
	}
}

// At 150% the stage is logical and the screenshot physical: the actors' rectangles grow outward
// to whole pixels, and at 1 they are as measured.
func TestStageActorsOnScreen(t *testing.T) {
	s := Stage{W: 853, Actors: []Rect{{X: 10, Y: 5, W: 21, H: 11, Why: "the clock"}}}
	got := s.OnScreen(1280)
	if len(got) != 1 || got[0].Why != "the clock" || got[0].X != 15 || got[0].Y != 7 || got[0].X+got[0].W < 47 || got[0].Y+got[0].H < 24 {
		t.Errorf("scaled: %+v", got)
	}
	s.W = 1280
	if got := s.OnScreen(1280); got[0] != s.Actors[0] {
		t.Errorf("unscaled: %+v", got)
	}
}

// One region an actor: the clock measured at two places a few pixels apart is one rectangle that
// holds both; an actor that did not move is one; two actors stay two.
func TestUnionActors(t *testing.T) {
	clock := func(x, w int) Rect { return Rect{X: x, Y: 16, W: w, H: 64, Why: "the clock"} }
	vitals := func(x, w int) Rect { return Rect{X: x, Y: 16, W: w, H: 64, Why: "the readings of Vitals"} }
	got := Union([]Rect{clock(1786, 258), vitals(2036, 428)}, []Rect{clock(1792, 258), vitals(2042, 422)})
	want := []Rect{clock(1786, 264), vitals(2036, 428)}
	if !slices.Equal(got, want) {
		t.Errorf("%+v, want %+v", got, want)
	}
	if got := Union([]Rect{clock(1786, 258)}, []Rect{clock(1786, 258)}); len(got) != 1 {
		t.Errorf("the same place twice: %+v", got)
	}
}

// NoiseOf runs on every view a run shoots: what it costs on the X9's screen.
func BenchmarkNoiseOf(b *testing.B) {
	img := solid(2880, 1800, tG)
	for y := range 1800 {
		for x := 0; x < 2880; x += 7 {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	for b.Loop() {
		NoiseOf(img, tPalette, 2)
	}
}
