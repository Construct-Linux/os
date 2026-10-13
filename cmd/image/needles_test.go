package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Construct-Linux/os/internal/needle"
	"github.com/Construct-Linux/os/internal/pixels"
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
var tScreen = needle.Screen{W: 100, H: 100, Scale: 1, StageW: 100, StageH: 100}

// tSpec is a whole spec for a test view; a needle's is approved.
func tSpec(approved bool, ignore, actors []needle.Rect) needle.Spec {
	s := needle.Spec{Group: needle.GroupShell, About: "a test view", Rules: []string{"S1"}, Scheme: "dark", Screen: tScreen, Ignore: ignore, Actors: actors}
	if approved {
		share := 0.01
		s.Approved = &needle.Approval{Build: "20261004.1", OS: "abc", Share: &share}
	}
	return s
}

func writeSpec(t *testing.T, p string, s needle.Spec) {
	t.Helper()
	if err := s.Write(p); err != nil {
		t.Fatal(err)
	}
}

// Every view the harness shoots says what it validates, by rules THEMING.md has.
func TestViewMetas(t *testing.T) {
	var metas []viewMeta
	for _, s := range shellViews {
		metas = append(metas, viewMeta{"shell-" + s.name, needle.GroupShell, s.about, s.rules})
	}
	for name, a := range gtkViewAbout {
		metas = append(metas, viewMeta{name, needle.GroupGTK, a.about, a.rules})
	}
	for _, a := range apps {
		metas = append(metas, appView(a.id))
	}
	for _, m := range metas {
		s := tSpec(false, nil, nil)
		s.Group, s.About, s.Rules = m.group, m.about, m.rules
		if err := s.Validate(false); err != nil {
			t.Errorf("%s: %v", m.name, err)
		}
	}
}

// A proposal makes the run's view the needle: its spec as the run wrote it, the needle's own
// regions kept, and how it was approved; the tolerance beside them; retired views gone.
func TestProposeNeedle(t *testing.T) {
	from, needles := t.TempDir(), t.TempDir()
	run := tSpec(false, nil, []needle.Rect{{X: 1, Y: 1, W: 2, H: 2, Why: "the clock"}})
	run.LF = needle.LF{Pass: 3, Fail: 1, Checks: []string{"accent areas shell-x"}}
	writeSpec(t, filepath.Join(from, "shell-x.json"), run)
	if err := pixels.WritePNG(filepath.Join(from, "shell-x.png"), solid(100, 100, tInk)); err != nil {
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
	got, err := needle.ReadSpec(filepath.Join(needles, "shell-x.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ignore) != 1 || got.Ignore[0].Why != "a face" || len(got.Actors) != 1 || got.LF.Fail != 1 ||
		got.Approved.Build != l.Version || got.Approved.OS != "abc" || got.Approved.New || *got.Approved.Share != 0.02 {
		t.Errorf("needle %+v approved %+v", got, got.Approved)
	}
	writeSpec(t, filepath.Join(from, "shell-y.json"), run)
	if err := pixels.WritePNG(filepath.Join(from, "shell-y.png"), solid(100, 100, tInk)); err != nil {
		t.Fatal(err)
	}
	if err := proposeNeedle(from, needles, l, lookView{Name: "shell-y", New: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := needle.ReadSpec(filepath.Join(needles, "shell-y.json"), true); err != nil || got.Approved.Share != nil || !got.Approved.New {
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

// A proposal's commit keeps the table alone; its pull request asks a person what no check measures,
// as boxes to tick, and names what lf held instead of asking it again.
func TestProposalBody(t *testing.T) {
	l := look{Version: "20261004.1", Needles: "abc"}
	table := proposalTable(l, []lookView{
		{Name: "shell-calendar", Share: 0.013, LF: needle.LF{Pass: 14}},
		{Name: "gtk-focus", New: true, LF: needle.LF{Pass: 2, Fail: 1, Checks: []string{"focus gtk"}}},
		{Name: "org.gnome.Maps", Share: 0.004, LF: needle.LF{Pass: 2}},
		{Name: "type-specimen", Share: 0.004},
	})
	for _, want := range []string{"| `shell-calendar` | 1.300% | 14 passed |", "| `gtk-focus` | new view | **1 failed**: focus gtk |",
		"| `org.gnome.Maps` | 0.400% | C3 only |", "| `type-specimen` | 0.400% | not measured |"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "- [ ]") {
		t.Errorf("the table asks:\n%s", table)
	}
	review := reviewChecklist()
	if strings.Count(review, "- [ ] ") != len(reviewAsked) || !strings.Contains(review, "- C3, gated by lf: `accent areas <view>`") {
		t.Errorf("review:\n%s", review)
	}
}

// A view's noise in the proposal's table, against its needle's when it has one.
func TestNoiseCell(t *testing.T) {
	if c := noiseCell(needle.Noise{Edges: 0.031, PaletteColors: 9}, &needle.Noise{Edges: 0.029, PaletteColors: 8}); c != "edges 3.10%, 9 colours (+0.20, +1)" {
		t.Errorf("cell %q", c)
	}
	if c := noiseCell(needle.Noise{Edges: 0.031, PaletteColors: 9}, nil); c != "edges 3.10%, 9 colours" {
		t.Errorf("cell without a needle's %q", c)
	}
}

// Up to 20 views are one proposal; more are one per group, in the groups' order.
func TestProposalBatches(t *testing.T) {
	few := []lookView{{Name: "a", Group: needle.GroupGTK}, {Name: "b", Group: needle.GroupShell}}
	if b := proposalBatches(few); len(b) != 1 || b[0].group != "" || len(b[0].views) != 2 {
		t.Errorf("few: %+v", b)
	}
	var many []lookView
	for i := range 25 {
		g := needle.GroupApp
		if i%5 == 0 {
			g = needle.GroupShell
		}
		many = append(many, lookView{Name: fmt.Sprint(i), Group: g})
	}
	b := proposalBatches(many)
	if len(b) != 2 || b[0].group != needle.GroupShell || len(b[0].views) != 5 || b[1].group != needle.GroupApp || len(b[1].views) != 20 {
		t.Errorf("many: %d batches %+v", len(b), b)
	}
}

// Only the run's views are compared: a picture the report keeps beside them (an installer's page)
// is no view, and listing it as a new one would stop needles-propose on its missing group.
func TestCompareNeedlesViewsOnly(t *testing.T) {
	dir, needles := t.TempDir(), t.TempDir()
	for _, f := range []string{"shell-x.png", "installer-done.png"} {
		if err := pixels.WritePNG(filepath.Join(dir, f), solid(10, 10, tInk)); err != nil {
			t.Fatal(err)
		}
	}
	s := tSpec(false, nil, nil)
	// shell-gone's shot failed: it is in the run, with no picture to compare.
	r := &report{dir: dir, views: map[string]*needle.Spec{"shell-x": &s, "shell-gone": &s}}
	r.compareNeedles(needles)
	raw, err := os.ReadFile(filepath.Join(dir, "look.json"))
	if err != nil {
		t.Fatal(err)
	}
	var l look
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Views) != 1 || l.Views[0].Name != "shell-x" || !l.Views[0].New || l.Views[0].Group != needle.GroupShell {
		t.Errorf("views %+v", l.Views)
	}
}
