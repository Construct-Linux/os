package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A view differs by the share of its compared pixels a theme change moved, not by noise, and
// leaves out what its .json ignores.
func TestCompareNeedle(t *testing.T) {
	dir := t.TempDir()
	ink := color.NRGBA{0x0B, 0x11, 0x16, 255}
	ref := filepath.Join(dir, "ref.png")
	if err := writePNG(ref, solid(100, 100, ink)); err != nil {
		t.Fatal(err)
	}
	shot := func(name string, img image.Image) string {
		p := filepath.Join(dir, name+".png")
		if err := writePNG(p, img); err != nil {
			t.Fatal(err)
		}
		return p
	}
	diff := filepath.Join(dir, "diff.png")

	// Noise under the channel tolerance: nothing differs.
	noisy := solid(100, 100, color.NRGBA{0x0B + 10, 0x11, 0x16, 255})
	if share, err := compareNeedle(ref, shot("noisy", noisy), "", diff); err != nil || share != 0 {
		t.Errorf("noise: %v, %v", share, err)
	}

	// A 10x10 square of another colour: 100 of the compared pixels.
	changed := solid(100, 100, ink)
	for y := 50; y < 60; y++ {
		for x := 10; x < 20; x++ {
			changed.SetNRGBA(x, y, color.NRGBA{0x1F, 0x2A, 0x33, 255})
		}
	}
	share, err := compareNeedle(ref, shot("changed", changed), "", diff)
	if err != nil {
		t.Fatal(err)
	}
	if want := 100.0 / 10000; share != want {
		t.Errorf("share = %v, want %v", share, want)
	}

	// The same square ignored by the view's .json: nothing differs.
	spec := filepath.Join(dir, "ref.json")
	if err := os.WriteFile(spec, []byte(`{"ignore":[{"x":10,"y":50,"w":10,"h":10,"why":"a clock face"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if share, err := compareNeedle(ref, shot("changed", changed), spec, diff); err != nil || share != 0 {
		t.Errorf("ignored: %v, %v", share, err)
	}

	// A clock whose text changed width: the needle drew it at x 10-20, this run at x 30-45. Each
	// side's measured actors leave out where it was there, so neither place differs.
	clockRef := solid(100, 100, ink)
	fill(clockRef, lfRect{10, 80, 10, 8}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	clockRefPath := filepath.Join(dir, "clock-ref.png")
	if err := writePNG(clockRefPath, clockRef); err != nil {
		t.Fatal(err)
	}
	clockSpec := filepath.Join(dir, "clock-ref.json")
	if err := os.WriteFile(clockSpec, []byte(`{"actors":[{"x":8,"y":78,"w":14,"h":12,"why":"the clock"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	moved := solid(100, 100, ink)
	fill(moved, lfRect{30, 80, 15, 8}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	movedPath := shot("moved", moved)
	if share, err := compareNeedle(clockRefPath, movedPath, clockSpec, diff); err != nil || share == 0 {
		t.Errorf("the run's clock, unmeasured: %v, %v", share, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "moved.json"), []byte(`{"actors":[{"x":28,"y":78,"w":19,"h":12,"why":"the clock"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if share, err := compareNeedle(clockRefPath, movedPath, clockSpec, diff); err != nil || share != 0 {
		t.Errorf("the clock, measured in both: %v, %v", share, err)
	}

	// Another size is a failure of its own.
	if _, err := compareNeedle(ref, shot("small", solid(50, 50, ink)), "", diff); err == nil {
		t.Error("a different size: no error")
	}
}

// A proposal's commit keeps the table alone; its pull request asks a person what no check measures,
// as boxes to tick, and names what lf held instead of asking it again.
func TestProposalBody(t *testing.T) {
	l := look{Version: "20261004.1", Needles: "abc"}
	table := proposalTable(l, []lookView{{Name: "shell-calendar", Share: 0.013}, {Name: "gtk-focus", New: true}})
	if !strings.Contains(table, "| `shell-calendar` | 1.300% |") || !strings.Contains(table, "| `gtk-focus` | new view |") || strings.Contains(table, "- [ ]") {
		t.Errorf("table:\n%s", table)
	}
	review := reviewChecklist()
	if strings.Count(review, "- [ ] ") != len(reviewAsked) || !strings.Contains(review, "- C3, gated by lf: `accent areas <view>`") {
		t.Errorf("review:\n%s", review)
	}
}

// At 150% the stage is logical and the screenshot physical: the actors' rectangles grow outward
// to whole pixels, and at 1 they are as measured.
func TestStageActorsOnScreen(t *testing.T) {
	s := stageActors{W: 853, Actors: []needleRect{{X: 10, Y: 5, W: 21, H: 11, Why: "the clock"}}}
	got := s.onScreen(1280)
	if len(got) != 1 || got[0].Why != "the clock" || got[0].X != 15 || got[0].Y != 7 || got[0].X+got[0].W < 47 || got[0].Y+got[0].H < 24 {
		t.Errorf("scaled: %+v", got)
	}
	s.W = 1280
	if got := s.onScreen(1280); got[0] != s.Actors[0] {
		t.Errorf("unscaled: %+v", got)
	}
}
