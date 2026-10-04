package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
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

// tScreen is a 100x100 test view's screen.
var tScreen = needleScreen{W: 100, H: 100, Scale: 1, StageW: 100, StageH: 100}

// tSpec is a whole spec for a test view; a needle's is approved.
func tSpec(needle bool, ignore, actors []needleRect) needleSpec {
	s := needleSpec{Group: groupShell, About: "a test view", Rules: []string{"S1"}, Scheme: "dark", Screen: tScreen, Ignore: ignore, Actors: actors}
	if needle {
		share := 0.01
		s.Approved = &needleApproval{Build: "20261004.1", OS: "abc", Share: &share}
	}
	return s
}

func writeSpec(t *testing.T, p string, s needleSpec) {
	t.Helper()
	if err := s.write(p); err != nil {
		t.Fatal(err)
	}
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
	spec := filepath.Join(dir, "ref.json")
	writeSpec(t, spec, tSpec(true, nil, nil))
	shot := func(name string, img image.Image) string {
		p := filepath.Join(dir, name+".png")
		if err := writePNG(p, img); err != nil {
			t.Fatal(err)
		}
		writeSpec(t, filepath.Join(dir, name+".json"), tSpec(false, nil, nil))
		return p
	}
	diff := filepath.Join(dir, "diff.png")

	// Noise under the channel tolerance: nothing differs.
	noisy := solid(100, 100, color.NRGBA{0x0B + 10, 0x11, 0x16, 255})
	if share, err := compareNeedle(ref, shot("noisy", noisy), spec, diff); err != nil || share != 0 {
		t.Errorf("noise: %v, %v", share, err)
	}

	// A 10x10 square of another colour: 100 of the compared pixels.
	changed := solid(100, 100, ink)
	for y := 50; y < 60; y++ {
		for x := 10; x < 20; x++ {
			changed.SetNRGBA(x, y, color.NRGBA{0x1F, 0x2A, 0x33, 255})
		}
	}
	share, err := compareNeedle(ref, shot("changed", changed), spec, diff)
	if err != nil {
		t.Fatal(err)
	}
	if want := 100.0 / 10000; share != want {
		t.Errorf("share = %v, want %v", share, want)
	}

	// The same square ignored by the needle's .json: nothing differs.
	ignoring := filepath.Join(dir, "ignoring.json")
	writeSpec(t, ignoring, tSpec(true, []needleRect{{X: 10, Y: 50, W: 10, H: 10, Why: "a clock face"}}, nil))
	if share, err := compareNeedle(ref, shot("changed", changed), ignoring, diff); err != nil || share != 0 {
		t.Errorf("ignored: %v, %v", share, err)
	}

	// A needle drawn on another screen, or one that does not say what it is, is no needle for it.
	other := tSpec(true, nil, nil)
	other.Screen.Scale, other.Screen.StageW, other.Screen.StageH = 2, 50, 50
	writeSpec(t, filepath.Join(dir, "other.json"), other)
	if share, err := compareNeedle(ref, shot("noisy", noisy), filepath.Join(dir, "other.json"), diff); err == nil || share != 1 {
		t.Errorf("another screen: %v, %v", share, err)
	}
	unapproved := tSpec(false, nil, nil)
	writeSpec(t, filepath.Join(dir, "unapproved.json"), unapproved)
	if _, err := compareNeedle(ref, shot("noisy", noisy), filepath.Join(dir, "unapproved.json"), diff); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("a needle not approved: %v", err)
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
	writeSpec(t, clockSpec, tSpec(true, nil, []needleRect{{X: 8, Y: 78, W: 14, H: 12, Why: "the clock"}}))
	moved := solid(100, 100, ink)
	fill(moved, lfRect{30, 80, 15, 8}, color.NRGBA{0xE6, 0xED, 0xF2, 255})
	movedPath := shot("moved", moved)
	if share, err := compareNeedle(clockRefPath, movedPath, clockSpec, diff); err != nil || share == 0 {
		t.Errorf("the run's clock, unmeasured: %v, %v", share, err)
	}
	writeSpec(t, filepath.Join(dir, "moved.json"), tSpec(false, nil, []needleRect{{X: 28, Y: 78, W: 19, H: 12, Why: "the clock"}}))
	if share, err := compareNeedle(clockRefPath, movedPath, clockSpec, diff); err != nil || share != 0 {
		t.Errorf("the clock, measured in both: %v, %v", share, err)
	}

	// Another size is a failure of its own.
	if _, err := compareNeedle(ref, shot("small", solid(50, 50, ink)), spec, diff); err == nil {
		t.Error("a different size: no error")
	}
}

// A spec says what its view is, every field of it; a needle also how it was approved, and a share
// only when it replaced one.
func TestSpecValidate(t *testing.T) {
	if err := tSpec(false, nil, nil).validate(false); err != nil {
		t.Errorf("a whole run spec: %v", err)
	}
	if err := tSpec(true, nil, nil).validate(true); err != nil {
		t.Errorf("a whole needle: %v", err)
	}
	bad := tSpec(true, nil, nil)
	bad.Group, bad.About, bad.Rules, bad.Scheme, bad.Screen = "misc", "", []string{"E2", "lf"}, "auto", needleScreen{}
	bad.Approved.Share = nil
	err := bad.validate(true)
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

// Every view the harness shoots says what it validates, by rules THEMING.md has.
func TestViewMetas(t *testing.T) {
	var metas []viewMeta
	for _, s := range shellViews {
		metas = append(metas, viewMeta{"shell-" + s.name, groupShell, s.about, s.rules})
	}
	for name, a := range gtkViewAbout {
		metas = append(metas, viewMeta{name, groupGTK, a.about, a.rules})
	}
	for _, a := range apps {
		metas = append(metas, appView(a.id))
	}
	for _, m := range metas {
		s := tSpec(false, nil, nil)
		s.Group, s.About, s.Rules = m.group, m.about, m.rules
		if err := s.validate(false); err != nil {
			t.Errorf("%s: %v", m.name, err)
		}
	}
}

// A proposal makes the run's view the needle: its spec as the run wrote it, the needle's own
// regions kept, and how it was approved; the tolerance beside them; retired views gone.
func TestProposeNeedle(t *testing.T) {
	from, needles := t.TempDir(), t.TempDir()
	run := tSpec(false, nil, []needleRect{{X: 1, Y: 1, W: 2, H: 2, Why: "the clock"}})
	run.LF = viewLF{Pass: 3, Fail: 1, Checks: []string{"accent areas shell-x"}}
	writeSpec(t, filepath.Join(from, "shell-x.json"), run)
	if err := writePNG(filepath.Join(from, "shell-x.png"), solid(100, 100, tInk)); err != nil {
		t.Fatal(err)
	}
	// The needle it replaces predates every field but its regions.
	if err := os.WriteFile(filepath.Join(needles, "shell-x.json"), []byte(`{"ignore":[{"x":5,"y":5,"w":5,"h":5,"why":"a face"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l := look{Version: "20261004.1", OS: "abc"}
	if err := proposeNeedle(from, needles, l, lookView{Name: "shell-x", Share: 0.02}); err != nil {
		t.Fatal(err)
	}
	got, err := readSpec(filepath.Join(needles, "shell-x.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ignore) != 1 || got.Ignore[0].Why != "a face" || len(got.Actors) != 1 || got.LF.Fail != 1 ||
		got.Approved.Build != l.Version || got.Approved.OS != "abc" || got.Approved.New || *got.Approved.Share != 0.02 {
		t.Errorf("needle %+v approved %+v", got, got.Approved)
	}
	writeSpec(t, filepath.Join(from, "shell-y.json"), run)
	if err := writePNG(filepath.Join(from, "shell-y.png"), solid(100, 100, tInk)); err != nil {
		t.Fatal(err)
	}
	if err := proposeNeedle(from, needles, l, lookView{Name: "shell-y", New: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := readSpec(filepath.Join(needles, "shell-y.json"), true); err != nil || got.Approved.Share != nil || !got.Approved.New {
		t.Errorf("a new view: %+v, %v", got.Approved, err)
	}
	if err := writeTolerance(needles); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(needles, toleranceFile)); !strings.Contains(string(raw), `"channel": 24`) || !strings.Contains(string(raw), `"share": 0.002`) {
		t.Errorf("tolerance: %s", raw)
	}
	for _, f := range []string{"desktop-dark.png", "desktop-dark.json"} {
		if err := os.WriteFile(filepath.Join(needles, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := retireNeedles(needles)
	if err != nil || len(gone) != 2 {
		t.Errorf("retired %v, %v", gone, err)
	}
	if gone, err := retireNeedles(needles); err != nil || len(gone) != 0 {
		t.Errorf("retired again %v, %v", gone, err)
	}
}

// The screen is the panel over the stage measured, the scale to the hundredth.
func TestScreenOf(t *testing.T) {
	if s := screenOf(2880, 1800, stageActors{W: 1440, H: 900}, 2); s != (needleScreen{2880, 1800, 2, 1440, 900}) {
		t.Errorf("200%%: %+v", s)
	}
	if s := screenOf(2880, 1800, stageActors{W: 1921, H: 1201}, 2); s.Scale != 1.5 {
		t.Errorf("150%%, a stage a pixel off: %+v", s)
	}
	if s := screenOf(2880, 1800, stageActors{}, 2); s != (needleScreen{2880, 1800, 2, 1440, 900}) {
		t.Errorf("unmeasured: %+v", s)
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

// An actor that stayed put is left out once; one that moved, at both places.
func TestUnionActors(t *testing.T) {
	clock := needleRect{X: 700, Y: 8, W: 60, H: 30, Why: "the clock"}
	vitals := needleRect{X: 900, Y: 8, W: 120, H: 30, Why: "the readings of Vitals"}
	moved := vitals
	moved.W = 128
	got := unionActors([]needleRect{vitals, clock}, []needleRect{clock, moved})
	want := []needleRect{clock, vitals, moved}
	if !slices.Equal(got, want) {
		t.Errorf("union %v, want %v", got, want)
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
