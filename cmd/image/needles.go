package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// A needle is how a view must look: the screenshot the harness took of it once someone looked at
// it and approved (github.com/Construct-Linux/needles, openQA's word for the same thing). Every
// run compares its screenshots with them; a view that changed beyond the tolerance fails, with
// the difference drawn; task needles:propose opens a pull request with it, and merging that is
// approving the new look.
const (
	// A pixel differs when a channel moved more than this: antialiasing and the GPU-less
	// renderer's rounding stay under it, a colour the theme changed does not.
	needleChannelDelta = 24
	// A view fails when more of its compared area than this differs: a few glyphs that moved
	// fail it, noise does not.
	needleMaxShare = 0.002
)

// needleRect is a region of a view left out of the comparison, and why.
type needleRect struct {
	X   int    `json:"x"`
	Y   int    `json:"y"`
	W   int    `json:"w"`
	H   int    `json:"h"`
	Why string `json:"why"`
}

// needleSpec is a view's <name>.json beside its <name>.png, in a run and in the needles, where the
// gallery reads it: every field is required, so a view says what it is without the harness's code.
//
// Group is the part of the harness that shoots the view (viewGroups); About, in a sentence, what
// it validates, and Rules by THEMING.md's ids; Scheme the colour scheme in force when it was shot;
// Screen the panel and the stage it was drawn on. Ignore is written by hand: regions a view's own
// content changes in. Actors is measured when the view is shot: where the clock, Vitals' readings,
// notifications' times and the lock clock were, which move with their text's width, so no fixed
// rectangle holds them; a comparison leaves out the needle's and the run's both, so text that grew
// or shrank between them is left out where it was in either. LF is what lf's checks said of the
// view in the run. Approved is the needle's alone: which build and commit made it, and how far it
// was from the needle it replaced.
type needleSpec struct {
	Group    string          `json:"group"`
	About    string          `json:"about"`
	Rules    []string        `json:"rules"`
	Scheme   string          `json:"scheme"`
	Screen   needleScreen    `json:"screen"`
	Ignore   []needleRect    `json:"ignore"`
	Actors   []needleRect    `json:"actors"`
	Approved *needleApproval `json:"approved,omitempty"`
	LF       viewLF          `json:"lf"`
}

// needleScreen is the screen a view was drawn on: the panel in physical pixels, the stage in
// logical ones, and the scale between them - 2 for the X9-15 at 200%, 1.5 for desktop-scale-150.
type needleScreen struct {
	W      int     `json:"w"`
	H      int     `json:"h"`
	Scale  float64 `json:"scale"`
	StageW int     `json:"stage_w"`
	StageH int     `json:"stage_h"`
}

// needleApproval is how a needle came to be: the build that drew it, the os commit that build was
// made from, and the share of the needle it replaced that differed - none when it was new.
type needleApproval struct {
	Build string   `json:"build"`
	OS    string   `json:"os"`
	Share *float64 `json:"share"`
	New   bool     `json:"new"`
}

// viewLF is lf's checks on one view: how many passed, how many failed, and the failed ones' names.
// None at all is a view lf does not measure, which only a person's review covers.
type viewLF struct {
	Pass   int      `json:"pass"`
	Fail   int      `json:"fail"`
	Checks []string `json:"checks"`
}

// The groups a view is shot in, each by its own part of the harness: the shell's views, the
// desktop over each wallpaper, libadwaita's own surfaces, the apps as they open, Settings' panels,
// the type specimen and the installer.
const (
	groupShell     = "shell"
	groupDesktop   = "desktop"
	groupGTK       = "gtk"
	groupApp       = "app"
	groupSettings  = "settings"
	groupType      = "type"
	groupInstaller = "installer"
)

var viewGroups = []string{groupShell, groupDesktop, groupGTK, groupApp, groupSettings, groupType, groupInstaller}

// themingRule is a THEMING.md rule's id: a rule a view names must be one there is.
var themingRule = regexp.MustCompile(`^(C|P|S|E|F|R|SP|T|TY|X|W)[0-9]+$`)

// viewMeta is what the code that shoots a view knows of it: its name, its group, what it
// validates and by which rules. The scheme and the screen are measured as it is shot.
type viewMeta struct {
	name, group, about string
	rules              []string
}

// validate says what a spec lacks; a needle must also say how it was approved.
func (s needleSpec) validate(needle bool) error {
	var bad []string
	if !slices.Contains(viewGroups, s.Group) {
		bad = append(bad, fmt.Sprintf("group %q is none of %v", s.Group, viewGroups))
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

// write saves the spec as JSON at p, its lists as lists even when empty: the gallery reads them.
func (s needleSpec) write(p string) error {
	s.Rules = append([]string{}, s.Rules...)
	s.Ignore = append([]needleRect{}, s.Ignore...)
	s.Actors = append([]needleRect{}, s.Actors...)
	s.LF.Checks = append([]string{}, s.LF.Checks...)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// needleBarFallback is what is left out of a view whose actors could not be measured, in a
// screenshot width px wide at scale: the top bar's inside from the middle on, its ring still
// compared. The bar is 6 px from the top and 36 high (lfBarHeight), in the stage's pixels.
func needleBarFallback(width, scale int) needleRect {
	scale = max(scale, 1)
	w := width / scale
	return needleRect{X: w / 2 * scale, Y: 8 * scale, W: (w/2 - 6) * scale, H: 32 * scale, Why: "the top bar's readings, not measured"}
}

// needleActors is the JavaScript that measures, in the running shell, what changes from one run to
// the next: inside the top bar, its clock and Vitals' readings, kept inside the bar's ring so the
// ring and the bar's surface around them are compared; with the calendar open, each notification's
// time; on the lock screen, its clock. Each grows by 2 px for antialiasing. Today's date and the
// month's days are not among them: the views are shot at viewsTime, so they are the same in every
// run, and leaving them out hid 9% of the calendar's views - the focus ring on a day among it.
//
// It answers them with the stage's size - at a fractional scale the stage is in logical pixels and
// the screenshot in physical ones - and the colour scheme in force, which a view records as it is
// rather than as the code meant it.
const needleActors = `
const out = [];
const box = a => {
    const [x, y] = a.get_transformed_position();
    const [w, h] = a.get_transformed_size();
    return {x: Math.floor(x) - 2, y: Math.floor(y) - 2, w: Math.ceil(w) + 4, h: Math.ceil(h) + 4};
};
const add = (a, why, clip) => {
    if (!a || !a.is_mapped())
        return;
    let r = box(a);
    if (clip) {
        const x0 = Math.max(r.x, clip.x), y0 = Math.max(r.y, clip.y);
        const x1 = Math.min(r.x + r.w, clip.x + clip.w), y1 = Math.min(r.y + r.h, clip.y + clip.h);
        if (x1 <= x0 || y1 <= y0)
            return;
        r = {x: x0, y: y0, w: x1 - x0, h: y1 - y0};
    }
    out.push({...r, why});
};
let bar = null;
if (Main.panel.is_mapped()) {
    const b = box(Main.panel);
    bar = {x: b.x + 4, y: b.y + 4, w: b.w - 8, h: b.h - 8};
}
const dm = Main.panel.statusArea.dateMenu;
add(dm?._clockDisplay, 'the clock', bar);
const vitals = Main.panel.statusArea.vitalsMenu;
add(vitals?.container ?? vitals, 'the readings of Vitals', bar);
if (dm?.menu.isOpen) {
    const times = a => {
        if (a.has_style_class_name?.('event-time'))
            add(a, 'the time of a notification');
        a.get_children().forEach(times);
    };
    times(dm.menu.box);
}
add(Main.screenShield?._dialog?._clock, 'the lock clock');
const scheme = new Gio.Settings({schema_id: 'org.gnome.desktop.interface'}).get_string('color-scheme');
return {w: global.stage.width, h: global.stage.height, scheme: scheme === 'prefer-dark' ? 'dark' : 'light', actors: out};`

// stageActors is what needleActors answers: the stage's size, the colour scheme and the actors on
// the stage.
type stageActors struct {
	W      float64      `json:"w"`
	H      float64      `json:"h"`
	Scheme string       `json:"scheme"`
	Actors []needleRect `json:"actors"`
}

// screenOf is the screen a screenshot w x h px was drawn on, over the stage seen; a stage not
// measured is taken as the panel over the run's scale.
func screenOf(w, h int, seen stageActors, scale int) needleScreen {
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
	return needleScreen{W: w, H: h, Scale: k, StageW: sw, StageH: sh}
}

// measureActors is what needleActors finds on the screen now.
func measureActors(g *guest) (stageActors, error) {
	var seen stageActors
	return seen, g.evalJSON(needleActors, &seen)
}

// onScreen is a stage's rectangles in a screenshot width px wide: scaled by the screenshot over
// the stage, outward to whole pixels so a scaled rectangle still covers what it held.
func (s stageActors) onScreen(width int) []needleRect {
	if s.W <= 0 || width <= 0 || math.Abs(float64(width)-s.W) < 0.5 {
		return s.Actors
	}
	k := float64(width) / s.W
	out := make([]needleRect, 0, len(s.Actors))
	for _, r := range s.Actors {
		x0, y0 := math.Floor(float64(r.X)*k), math.Floor(float64(r.Y)*k)
		x1, y1 := math.Ceil(float64(r.X+r.W)*k), math.Ceil(float64(r.Y+r.H)*k)
		out = append(out, needleRect{X: int(x0), Y: int(y0), W: int(x1 - x0), H: int(y1 - y0), Why: r.Why})
	}
	return out
}

// shotLook saves the screen as m.name.png, as shot does, and beside it m.name.json, the view's
// needleSpec: what m says of it, the scheme and the screen it was drawn on, and where the actors
// whose content changes from run to run were, measured before and after the screenshot - the
// clock may tick between them. A view whose actors cannot be measured leaves out the top bar's
// inside instead, and fails a check that says so. The spec is kept for lf's results on the view,
// which writeViews adds once every view is measured.
func (r *report) shotLook(v *vm, g *guest, m viewMeta) {
	name := m.name
	before, errBefore := measureActors(g)
	r.shot(v, name)
	after, errAfter := measureActors(g)
	width, height := 0, 0
	if f, err := os.Open(filepath.Join(r.dir, name+".png")); err == nil {
		if c, err := png.DecodeConfig(f); err == nil {
			width, height = c.Width, c.Height
		}
		f.Close()
	}
	seen := after
	if errAfter != nil {
		seen = before
	}
	was, is := before.onScreen(width), after.onScreen(width)
	spec := &needleSpec{Group: m.group, About: m.about, Rules: m.rules, Scheme: seen.Scheme,
		Screen: screenOf(width, height, seen, r.scale), Actors: unionActors(was, is)}
	if err := errors.Join(errBefore, errAfter); err != nil {
		spec.Actors = append(spec.Actors, needleBarFallback(width, r.scale))
		r.add("look: "+name+" actors", false, "the clock and Vitals could not be measured: "+oneLine(err.Error()))
	} else if !slices.Equal(sortedActors(was), sortedActors(is)) {
		// Both places are left out, so the comparison holds; said, because a view whose bar
		// changed while it was shot may have been shot half-drawn.
		r.add("look: "+name+" actors", true, fmt.Sprintf("moved during the capture: %v, then %v", was, is))
	}
	if r.views == nil {
		r.views = map[string]*needleSpec{}
	}
	r.views[name] = spec
	if err := spec.write(filepath.Join(r.dir, name+".json")); err != nil {
		r.add("look: "+name+" spec", false, err.Error())
	}
}

// viewCheck counts an lf check towards the view it measured, when it measured one.
func (r *report) viewCheck(view, check string, ok bool) {
	s := r.views[view]
	if s == nil {
		return
	}
	if ok {
		s.LF.Pass++
		return
	}
	s.LF.Fail++
	s.LF.Checks = append(s.LF.Checks, check)
}

// writeViews writes every view's spec again, now with what lf said of it.
func (r *report) writeViews() {
	for name, s := range r.views {
		if err := s.write(filepath.Join(r.dir, name+".json")); err != nil {
			r.add("look: "+name+" spec", false, err.Error())
		}
	}
}

// sortedActors is rs in one order, a copy: by place, then size, then why.
func sortedActors(rs []needleRect) []needleRect {
	out := slices.Clone(rs)
	slices.SortFunc(out, func(a, b needleRect) int {
		return cmp.Or(cmp.Compare(a.X, b.X), cmp.Compare(a.Y, b.Y), cmp.Compare(a.W, b.W), cmp.Compare(a.H, b.H), cmp.Compare(a.Why, b.Why))
	})
	return out
}

// unionActors is where either measurement found an actor, each rectangle once: an actor that did
// not move between them is the same rectangle twice, which was half of every view's actors.
func unionActors(a, b []needleRect) []needleRect {
	return slices.Compact(sortedActors(slices.Concat(a, b)))
}

// readSpec reads the needleSpec at p and validates it, as a needle's when needle is set: every
// view has one, in a run and in the needles.
func readSpec(p string, needle bool) (needleSpec, error) {
	var spec needleSpec
	raw, err := os.ReadFile(p)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("%s: %w", p, err)
	}
	if err := spec.validate(needle); err != nil {
		return spec, fmt.Errorf("%s: %w", p, err)
	}
	return spec, nil
}

// lookResult is one view against its needle.
type lookResult struct {
	name  string
	share float64 // of the compared area that differs
	err   error
}

// look is a run's comparison, _output/test/look.json: which build, from which os commit (the
// checkout the harness ran from), against which needles, and each view - what needles-propose
// turns into a pull request in the needles repository.
type look struct {
	Version string     `json:"version"`
	OS      string     `json:"os_commit"`
	Needles string     `json:"needles_commit"`
	Views   []lookView `json:"views"`
}

// lookView is one view of a run against its needle: how much differs, and what lf said of it - its
// counts, and the sweep's solid accent areas and accent share, which the counts alone do not show.
type lookView struct {
	Name  string    `json:"name"`
	Group string    `json:"group"`
	Share float64   `json:"share"`
	New   bool      `json:"new,omitempty"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	LF    viewLF    `json:"lf"`
	Sweep lookSweep `json:"sweep"`
}

// lookSweep is C3's sweep of a view: its solid accent areas larger than a mark, in the
// screenshot's pixels, and the share of it that is the full accent.
type lookSweep struct {
	Areas []sweepArea `json:"areas"`
	Share float64     `json:"share"`
}

type sweepArea struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// viewSweep keeps what the sweep found on a view, for look.json.
func (r *report) viewSweep(view string, areas []lfRect, share float64) {
	if r.sweeps == nil {
		r.sweeps = map[string]lookSweep{}
	}
	s := lookSweep{Areas: []sweepArea{}, Share: share}
	for _, a := range areas {
		s.Areas = append(s.Areas, sweepArea(a))
	}
	r.sweeps[view] = s
}

// lookViewOf is the run's view name in look.json, with what the run knows of it.
func (r *report) lookViewOf(name string) lookView {
	v := lookView{Name: name, Sweep: r.sweeps[name], LF: viewLF{Checks: []string{}}}
	if v.Sweep.Areas == nil {
		v.Sweep.Areas = []sweepArea{}
	}
	if s := r.views[name]; s != nil {
		v.Group, v.LF = s.Group, s.LF
	}
	return v
}

// compareNeedles compares every screenshot in dir that has a needle in needles, writes each
// difference to dir/diff/<name>.png and the side by side to dir/look.html, and reports a check
// per view. Screenshots without a needle are listed as new. Every view's spec is written first,
// with what lf said of it: it is the run's record of the view, needles or not.
func (r *report) compareNeedles(needles string) {
	r.writeViews()
	if needles == "" {
		r.skip("look", "no needles: NEEDLES names a checkout of github.com/Construct-Linux/needles")
		return
	}
	shots, err := filepath.Glob(filepath.Join(r.dir, "*.png"))
	if err != nil || len(shots) == 0 {
		r.add("look", false, "no screenshots in "+r.dir)
		return
	}
	sort.Strings(shots)
	if err := os.MkdirAll(filepath.Join(r.dir, "diff"), 0o755); err != nil {
		r.add("look", false, err.Error())
		return
	}
	var results []lookResult
	var unmatched []string
	for _, shot := range shots {
		name := strings.TrimSuffix(filepath.Base(shot), ".png")
		ref := filepath.Join(needles, name+".png")
		if _, err := os.Stat(ref); errors.Is(err, fs.ErrNotExist) {
			unmatched = append(unmatched, name)
			continue
		}
		share, err := compareNeedle(ref, shot, filepath.Join(needles, name+".json"), filepath.Join(r.dir, "diff", name+".png"))
		results = append(results, lookResult{name: name, share: share, err: err})
		switch {
		case err != nil:
			r.add("look: "+name, false, err.Error())
		default:
			r.add("look: "+name, share <= needleMaxShare, fmt.Sprintf("%.3f%% differs (diff/%s.png)", share*100, name))
		}
	}
	if len(unmatched) > 0 {
		r.skip("look: new views", strings.Join(unmatched, " ")+": no needle yet (task needles:propose)")
	}
	_ = os.WriteFile(filepath.Join(r.dir, "look.html"), []byte(lookPage(needles, results)), 0o644)
	l := look{Version: builtVersion(), OS: gitHead("."), Needles: gitHead(needles)}
	for _, x := range results {
		v := r.lookViewOf(x.name)
		v.Share, v.OK = x.share, x.err == nil && x.share <= needleMaxShare
		if x.err != nil {
			v.Error = x.err.Error()
		}
		l.Views = append(l.Views, v)
	}
	for _, n := range unmatched {
		v := r.lookViewOf(n)
		v.New = true
		l.Views = append(l.Views, v)
	}
	if b, err := json.MarshalIndent(l, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(r.dir, "look.json"), b, 0o644)
	}
}

// builtVersion is the version of the stick the harness booted (_output/usb/built.json).
func builtVersion() string {
	var b struct {
		Version string `json:"version"`
	}
	raw, _ := os.ReadFile(filepath.Join(out, "usb", "built.json"))
	_ = json.Unmarshal(raw, &b)
	return b.Version
}

// gitHead is the commit a checkout is at, or "" when it is none.
func gitHead(dir string) string {
	o, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(o))
}

// compareNeedle is the share of ref's pixels, outside the ignored regions, that differ in shot;
// it draws them red over ref dimmed into diff. What is ignored is the needle's spec (specPath) and
// the actors measured with the shot (its own .json beside it).
func compareNeedle(ref, shot, specPath, diff string) (float64, error) {
	a, err := readPNG(ref)
	if err != nil {
		return 0, err
	}
	b, err := readPNG(shot)
	if err != nil {
		return 0, err
	}
	if a.Bounds() != b.Bounds() {
		return 1, fmt.Errorf("%v, the needle %v: the screen size changed", b.Bounds().Size(), a.Bounds().Size())
	}
	// A needle that cannot say what it is, or was drawn on another screen, is no needle for this
	// view: all of it differs.
	spec, err := readSpec(specPath, true)
	if err != nil {
		return 1, err
	}
	run, err := readSpec(strings.TrimSuffix(shot, ".png")+".json", false)
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
			ca := color.NRGBAModel.Convert(a.At(x, y)).(color.NRGBA)
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
			cb := color.NRGBAModel.Convert(b.At(x, y)).(color.NRGBA)
			if channelDelta(ca, cb) > needleChannelDelta {
				differ++
				dim = color.NRGBA{255, 40, 40, 255}
			}
			out.SetNRGBA(x, y, dim)
		}
	}
	if err := writePNG(diff, out); err != nil {
		return 0, err
	}
	if compared == 0 {
		return 0, errors.New("everything is ignored")
	}
	return float64(differ) / float64(compared), nil
}

func channelDelta(a, b color.NRGBA) int {
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

func readPNG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// lookPage is the side by side: each view's needle, this run's screenshot and their difference,
// the failing first.
func lookPage(needles string, results []lookResult) string {
	sort.SliceStable(results, func(i, j int) bool { return results[i].share > results[j].share })
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>Look</title><style>
body{background:#0B1116;color:#E6EDF2;font:14px system-ui;margin:16px}
section{margin:0 0 28px}h2{font-size:15px;margin:0 0 6px}.bad{color:#FF7A90}
.row{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}img{width:100%;border:1px solid #2A3843}
small{color:#9FB3C0}</style><h1>Look: this run against the needles</h1>`)
	for _, x := range results {
		cls, note := "", fmt.Sprintf("%.3f%% differs", x.share*100)
		if x.err != nil {
			cls, note = "bad", x.err.Error()
		} else if x.share > needleMaxShare {
			cls = "bad"
		}
		n := html.EscapeString(x.name)
		fmt.Fprintf(&b, `<section><h2 class="%s">%s <small>%s</small></h2><div class="row">`+
			`<figure><img src="file://%s"><figcaption><small>needle</small></figcaption></figure>`+
			`<figure><img src="%s.png"><figcaption><small>this run</small></figcaption></figure>`+
			`<figure><img src="diff/%s.png"><figcaption><small>differs (red), ignored (blue)</small></figcaption></figure>`+
			`</div></section>`, cls, n, html.EscapeString(note), html.EscapeString(filepath.Join(needles, x.name+".png")), n, n)
	}
	return b.String()
}

// needlesPropose opens a pull request in the needles repository with the views this run drew
// differently from their needles, or that have none: GitHub shows each image's difference (side by
// side, swipe, onion skin), and merging it is approving the new look. A run whose views all match
// proposes nothing. The branch is look/<version>, from the needles' main. Each needle it writes
// carries its whole spec and its approval; tolerance.json is written beside them, and the retired
// views' needles go.
func needlesPropose(args []string) error {
	fl := flag.NewFlagSet("needles-propose", flag.ExitOnError)
	from := fl.String("from", filepath.Join(out, "test"), "the run")
	needles := fl.String("needles", "", "the needles checkout (NEEDLES)")
	_ = fl.Parse(args)
	if *needles == "" {
		return errors.New("needles-propose: -needles names the needles checkout (NEEDLES)")
	}
	raw, err := os.ReadFile(filepath.Join(*from, "look.json"))
	if err != nil {
		return fmt.Errorf("needles-propose: %w: run the harness with NEEDLES set first", err)
	}
	var l look
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	var changed []lookView
	for _, v := range l.Views {
		if v.OK {
			continue
		}
		if !slices.Contains(viewGroups, v.Group) {
			return fmt.Errorf("needles-propose: %s is in group %q, none of %v", v.Name, v.Group, viewGroups)
		}
		changed = append(changed, v)
	}
	if len(changed) == 0 {
		fmt.Println("every view matches its needle: nothing to propose")
		return nil
	}
	if l.Version == "" || l.OS == "" {
		return fmt.Errorf("needles-propose: look.json names build %q at os commit %q: a needle records both", l.Version, l.OS)
	}
	git := func(args ...string) error {
		c := exec.Command("git", append([]string{"-C", *needles}, args...)...)
		var e bytes.Buffer
		c.Stderr = &e
		if err := c.Run(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(e.String()))
		}
		return nil
	}
	if err := git("fetch", "-q", "origin"); err != nil {
		return err
	}
	// Back on main whatever happens: the checkout is where the next run compares from.
	defer func() { _ = git("checkout", "-q", "main") }()
	for i, b := range proposalBatches(changed) {
		branch, title := "look/"+l.Version, fmt.Sprintf("Look of %s: %d view(s)", l.Version, len(b.views))
		if b.group != "" {
			branch += "/" + b.group
			title = fmt.Sprintf("Look of %s, %s: %d view(s)", l.Version, b.group, len(b.views))
		}
		if err := git("checkout", "-q", "-B", branch, "origin/main"); err != nil {
			return err
		}
		var paths []string
		// What is the whole repository's goes with the first proposal alone.
		if i == 0 {
			if err := writeTolerance(*needles); err != nil {
				return err
			}
			gone, err := retireNeedles(*needles)
			if err != nil {
				return err
			}
			paths = append([]string{toleranceFile}, gone...)
		}
		for _, v := range b.views {
			if err := proposeNeedle(*from, *needles, l, v); err != nil {
				return err
			}
			paths = append(paths, v.Name+".png", v.Name+".json")
		}
		// -A stages the retired needles' removal with the rest.
		if err := git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
			return err
		}
		table := proposalTable(l, b.views)
		if err := git("commit", "-q", "-m", title, "-m", table); err != nil {
			return err
		}
		if err := git("push", "-q", "-f", "origin", branch); err != nil {
			return err
		}
		pr := exec.Command("gh", "pr", "create", "--title", title, "--body", table+"\n"+reviewChecklist(), "--head", branch, "--base", "main")
		pr.Dir = *needles
		pr.Stdout, pr.Stderr = os.Stdout, os.Stderr
		if err := pr.Run(); err != nil {
			return err
		}
	}
	return nil
}

// needlesPerProposal is the most views one pull request asks a person to review: a 70-view
// proposal could not be reviewed, and was merged without one.
const needlesPerProposal = 20

// proposalBatch is the views one pull request proposes, and the group they are, when split.
type proposalBatch struct {
	group string
	views []lookView
}

// proposalBatches is one proposal for up to needlesPerProposal views, else one per group, in
// viewGroups' order: a group is what a reviewer judges together.
func proposalBatches(views []lookView) []proposalBatch {
	if len(views) <= needlesPerProposal {
		return []proposalBatch{{views: views}}
	}
	var out []proposalBatch
	for _, g := range viewGroups {
		b := proposalBatch{group: g}
		for _, v := range views {
			if v.Group == g {
				b.views = append(b.views, v)
			}
		}
		if len(b.views) > 0 {
			out = append(out, b)
		}
	}
	return out
}

// toleranceFile is needleChannelDelta and needleMaxShare at the needles' root, for the gallery to
// show what "matches" means without reading this file.
const toleranceFile = "tolerance.json"

func writeTolerance(needles string) error {
	b, err := json.MarshalIndent(map[string]any{"channel": needleChannelDelta, "share": needleMaxShare}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(needles, toleranceFile), append(b, '\n'), 0o644)
}

// proposeNeedle makes the run's view v the needle in the needles checkout: its screenshot, and its
// spec as the run wrote it, with the needle's hand-written regions kept and how it was approved.
func proposeNeedle(from, needles string, l look, v lookView) error {
	b, err := os.ReadFile(filepath.Join(from, v.Name+".png"))
	if err != nil {
		return err
	}
	spec, err := readSpec(filepath.Join(from, v.Name+".json"), false)
	if err != nil {
		return err
	}
	// The regions are a person's and stay; the rest of the old spec is the old run's. Read as
	// regions alone: the needle it replaces may predate any other field.
	var old struct {
		Ignore []needleRect `json:"ignore"`
	}
	raw, err := os.ReadFile(filepath.Join(needles, v.Name+".json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &old); err != nil {
			return fmt.Errorf("%s.json in the needles: %w", v.Name, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	spec.Ignore = old.Ignore
	spec.Approved = &needleApproval{Build: l.Version, OS: l.OS, New: v.New}
	if !v.New {
		share := v.Share
		spec.Approved.Share = &share
	}
	if err := spec.validate(true); err != nil {
		return fmt.Errorf("%s: %w", v.Name, err)
	}
	if err := os.WriteFile(filepath.Join(needles, v.Name+".png"), b, 0o644); err != nil {
		return err
	}
	return spec.write(filepath.Join(needles, v.Name+".json"))
}

// retiredViews are views no run shoots any more, whose needles a proposal removes. desktop-dark was
// the desktop view again: the default dark wallpaper (construct-dark.png) under the same bars,
// 0.165% from it, under the tolerance - it held nothing desktop did not.
var retiredViews = []string{"desktop-dark"}

// retireNeedles removes the retired views' needles from the checkout, and answers the files it
// removed.
func retireNeedles(needles string) ([]string, error) {
	var gone []string
	for _, name := range retiredViews {
		for _, f := range []string{name + ".png", name + ".json"} {
			err := os.Remove(filepath.Join(needles, f))
			switch {
			case err == nil:
				gone = append(gone, f)
			case !errors.Is(err, fs.ErrNotExist):
				return gone, err
			}
		}
	}
	return gone, nil
}

// proposalTable is what a proposal changes, view by view: the commit's message, and the top of
// the pull request. Its lf column is what the run's checks held on each view, so a reviewer sees
// which views only their own eyes cover.
func proposalTable(l look, views []lookView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Build `%s`, against needles `%s`.\n\n| View | Differs | lf |\n|---|---|---|\n", l.Version, l.Needles)
	for _, v := range views {
		state := fmt.Sprintf("%.3f%%", v.Share*100)
		switch {
		case v.New:
			state = "new view"
		case v.Error != "":
			state = v.Error
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", v.Name, state, lfCell(v.LF))
	}
	return b.String()
}

// lfCell is lf's word on a view for the table: what failed, how many passed, or that it measured
// nothing but the sweep, or nothing at all.
func lfCell(r viewLF) string {
	switch {
	case r.Fail > 0:
		return fmt.Sprintf("**%d failed**: %s", r.Fail, strings.Join(r.Checks, ", "))
	case r.Pass == 0:
		return "not measured"
	case r.Pass <= lfSweepChecks:
		return "C3 only"
	}
	return fmt.Sprintf("%d passed", r.Pass)
}

// reviewAsked is what a person judges before merging a proposal, because no check can: needles say
// that a view changed, lf that it keeps the rules it can measure, neither that it is still good.
// The checklist is in the body needles-propose writes: a template in the needles repository never
// reaches a pull request opened with gh --body.
var reviewAsked = []string{
	"Where a view has a solid accent area, it is that view's one right primary action (lf counts the areas, not which control fills them)",
	"The chrome reads quieter than the content it frames",
	"No new visual primitive: no shape, icon style or radius that no view had before",
	"No new persistent badge or indicator",
	"Upstream's branding is kept wherever the palette does not need it changed",
}

// reviewGated are the rules lf already measures on the views, by its checks' names: not asked
// again, since a run that broke one failed it.
var reviewGated = []struct{ rules, checks string }{
	{"C3", "`accent areas <view>` and `accent share <view>`, on every view"},
	{"E2, S1", "`surface`, `ring` and `edge` of `top bar (<view>)` and `dock (<view>)`, over every wallpaper"},
	{"F1, C8", "`focus quick toggle style`, `focus quick toggle drawn`, `focus calendar day`, `today calendar`, `css focus G2`, `css rings C8`"},
	{"S2, S4", "`surface quick settings`, `surface calendar`, `submenu quick settings`, `ground <view>`"},
	{"S2 states, F2", "`states quick toggles`, `state gtk-hover button`, `state gtk-pressed button`, `focus gtk-focus`, `error lock prompt`"},
	{"S3, E3", "`window installer`, `rule installer headerbar`, `window maximized`"},
	{"S5, S6, C11, C2 on light", "`desk settings-light`, `ring settings-light window`, `accent settings-light window`, `shelf settings-light`, `selected row settings-light`, `rule settings-light headerbar`"},
	{"C1, T2", "`css palette G3`, `css weights G6`, `css weights G6 gtk`"},
	{"R1, SP2", "`radius <surface>`, `spacing bars`, `spacing top bar buttons`, `spacing menus`"},
	{"X2", "`icons apps`"},
}

// reviewChecklist is the pull request's review: boxes for what a person judges, and lines for
// what lf gated.
func reviewChecklist() string {
	var b strings.Builder
	b.WriteString("## Review\n\nMerging approves these as how the views must look: tick each box once every view above has been looked at.\n\n")
	for _, q := range reviewAsked {
		fmt.Fprintf(&b, "- [ ] %s\n", q)
	}
	b.WriteString("\nAlready held by the run that shot them:\n\n")
	for _, g := range reviewGated {
		fmt.Fprintf(&b, "- %s, gated by lf: %s\n", g.rules, g.checks)
	}
	return b.String()
}
