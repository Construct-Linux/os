package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"image/color"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/needle"
	"github.com/Construct-Linux/os/internal/pixels"
	"github.com/Construct-Linux/os/internal/qemu"
)

// viewMeta is what the code that shoots a view knows of it: its name, its group, what it
// validates and by which rules. The scheme and the screen are measured as it is shot.
type viewMeta struct {
	name, group, about string
	rules              []string
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

// measureActors is what needleActors finds on the screen now.
func measureActors(g *guest) (needle.Stage, error) {
	var seen needle.Stage
	return seen, g.evalJSON(needleActors, &seen)
}

// shotLook saves the screen as m.name.png, as shot does, and beside it m.name.json, the view's
// needle.Spec: what m says of it, the scheme and the screen it was drawn on, and where the actors
// whose content changes from run to run were, measured before and after the screenshot - the
// clock may tick between them. A view whose actors cannot be measured leaves out the top bar's
// inside instead, and fails a check that says so. The spec is kept for lf's results on the view,
// which writeViews adds once every view is measured.
func (r *report) shotLook(v *qemu.VM, g *guest, m viewMeta) {
	name := m.name
	before, errBefore := measureActors(g)
	r.shot(v, name)
	after, errAfter := measureActors(g)
	width, height := 0, 0
	var noise needle.Noise
	if img, err := pixels.ReadPNG(filepath.Join(r.dir, name+".png")); err == nil {
		width, height = img.Bounds().Dx(), img.Bounds().Dy()
		var palette []color.NRGBA
		if r.pal != nil {
			palette = slices.Collect(maps.Values(r.pal.colors))
		}
		noise = needle.NoiseOf(img, palette, lfSurfaceTol)
	}
	seen := after
	if errAfter != nil {
		seen = before
	}
	was, is := before.OnScreen(width), after.OnScreen(width)
	spec := &needle.Spec{Group: m.group, About: m.about, Rules: m.rules, Scheme: seen.Scheme,
		Screen: needle.ScreenOf(width, height, seen, r.scale), Actors: needle.Union(was, is), Noise: noise}
	if err := errors.Join(errBefore, errAfter); err != nil {
		spec.Actors = append(spec.Actors, needle.BarFallback(width, r.scale))
		r.add("look: "+name+" actors", false, "the clock and Vitals could not be measured: "+oneLine(err.Error()))
	} else if !slices.Equal(needle.Sorted(was), needle.Sorted(is)) {
		// Both places are left out, so the comparison holds; said, because a view whose bar
		// changed while it was shot may have been shot half-drawn.
		r.add("look: "+name+" actors", true, fmt.Sprintf("moved during the capture: %v, then %v", was, is))
	}
	if r.views == nil {
		r.views = map[string]*needle.Spec{}
	}
	r.views[name] = spec
	if err := spec.Write(filepath.Join(r.dir, name+".json")); err != nil {
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
		if err := s.Write(filepath.Join(r.dir, name+".json")); err != nil {
			r.add("look: "+name+" spec", false, err.Error())
		}
	}
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
	Name  string       `json:"name"`
	Group string       `json:"group"`
	Share float64      `json:"share"`
	New   bool         `json:"new,omitempty"`
	OK    bool         `json:"ok"`
	Error string       `json:"error,omitempty"`
	LF    needle.LF    `json:"lf"`
	Sweep lookSweep    `json:"sweep"`
	Noise needle.Noise `json:"noise"`
	// NeedleNoise is the needle's, for the change; none when the view has no valid needle.
	NeedleNoise *needle.Noise `json:"needle_noise,omitempty"`
}

// lookSweep is C3's sweep of a view: its solid accent areas larger than a mark, in the
// screenshot's pixels, and the share of it that is the full accent.
type lookSweep struct {
	Areas []pixels.Rect `json:"areas"`
	Share float64       `json:"share"`
}

// viewSweep keeps what the sweep found on a view, for look.json.
func (r *report) viewSweep(view string, areas []pixels.Rect, share float64) {
	if r.sweeps == nil {
		r.sweeps = map[string]lookSweep{}
	}
	// A list even when empty: look.json's readers take it as one.
	r.sweeps[view] = lookSweep{Areas: append([]pixels.Rect{}, areas...), Share: share}
}

// lookViewOf is the run's view name in look.json, with what the run knows of it.
func (r *report) lookViewOf(name string) lookView {
	v := lookView{Name: name, Sweep: r.sweeps[name], LF: needle.LF{Checks: []string{}}}
	if v.Sweep.Areas == nil {
		v.Sweep.Areas = []pixels.Rect{}
	}
	if s := r.views[name]; s != nil {
		v.Group, v.LF, v.Noise = s.Group, s.LF, s.Noise
	}
	return v
}

// compareNeedles compares every view the run shot that has a needle in needles, writes each
// difference to dir/diff/<name>.png and the side by side to dir/look.html, and reports a check
// per view. Views without a needle are listed as new. Every view's spec is written first,
// with what lf said of it: it is the run's record of the view, needles or not.
func (r *report) compareNeedles(needles string) {
	r.writeViews()
	if needles == "" {
		r.skip("look", "no needles: NEEDLES names a checkout of github.com/Construct-Linux/needles")
		return
	}
	// The run's views, not every PNG in dir: the report also keeps pictures that are no view
	// (the installer's pages), which have no group and would stop needles-propose.
	names := slices.Sorted(maps.Keys(r.views))
	if len(names) == 0 {
		r.add("look", false, "no views shot in "+r.dir)
		return
	}
	if err := os.MkdirAll(filepath.Join(r.dir, "diff"), 0o755); err != nil {
		r.add("look", false, err.Error())
		return
	}
	var results []lookResult
	var unmatched []string
	for _, name := range names {
		shot := filepath.Join(r.dir, name+".png")
		// A view whose shot failed was reported by shot; there is nothing to compare.
		if _, err := os.Stat(shot); err != nil {
			continue
		}
		ref := filepath.Join(needles, name+".png")
		if _, err := os.Stat(ref); errors.Is(err, fs.ErrNotExist) {
			unmatched = append(unmatched, name)
			continue
		}
		share, err := needle.Compare(ref, shot, filepath.Join(needles, name+".json"), filepath.Join(r.dir, "diff", name+".png"))
		results = append(results, lookResult{name: name, share: share, err: err})
		switch {
		case err != nil:
			r.add("look: "+name, false, err.Error())
		default:
			r.add("look: "+name, share <= needle.MaxShare, fmt.Sprintf("%.3f%% differs (diff/%s.png)", share*100, name))
		}
	}
	if len(unmatched) > 0 {
		r.skip("look: new views", strings.Join(unmatched, " ")+": no needle yet (task needles:propose)")
	}
	_ = os.WriteFile(filepath.Join(r.dir, "look.html"), []byte(lookPage(needles, results)), 0o644)
	l := look{Version: builtVersion(), OS: gitHead("."), Needles: gitHead(needles)}
	for _, x := range results {
		v := r.lookViewOf(x.name)
		v.Share, v.OK = x.share, x.err == nil && x.share <= needle.MaxShare
		if spec, err := needle.ReadSpec(filepath.Join(needles, x.name+".json"), true); err == nil {
			v.NeedleNoise = &spec.Noise
		}
		if x.err != nil {
			v.Error = x.err.Error()
			// The pull request's table says it in a line; the report keeps the whole of it.
			if errors.As(x.err, new(needle.SpecError)) {
				v.Error = "its needle's .json is not as the harness writes it: the view is approved again"
			}
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
		} else if x.share > needle.MaxShare {
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
		if !slices.Contains(needle.Groups, v.Group) {
			return fmt.Errorf("needles-propose: %s is in group %q, none of %v", v.Name, v.Group, needle.Groups)
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
		// A proposal run again for the same build updates its pull request, which the branch's
		// push already did for the commit. By REST: gh pr edit asks GraphQL for classic projects,
		// which GitHub refuses.
		body := table + "\n" + reviewChecklist()
		pr := exec.Command("gh", "pr", "create", "--title", title, "--body", body, "--head", branch, "--base", "main")
		open := exec.Command("gh", "pr", "view", branch, "--json", "state,number", "--jq", `select(.state == "OPEN") | .number`)
		open.Dir = *needles
		if n, err := open.Output(); err == nil && strings.TrimSpace(string(n)) != "" {
			pr = exec.Command("gh", "api", "-X", "PATCH", "--silent", "repos/{owner}/{repo}/pulls/"+strings.TrimSpace(string(n)),
				"-f", "title="+title, "-f", "body="+body)
		}
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
// needle.Groups' order: a group is what a reviewer judges together.
func proposalBatches(views []lookView) []proposalBatch {
	if len(views) <= needlesPerProposal {
		return []proposalBatch{{views: views}}
	}
	var out []proposalBatch
	for _, g := range needle.Groups {
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

// toleranceFile is needle.ChannelDelta and needle.MaxShare at the needles' root, for the gallery to
// show what "matches" means without reading this file.
const toleranceFile = "tolerance.json"

func writeTolerance(needles string) error {
	b, err := json.MarshalIndent(map[string]any{"channel": needle.ChannelDelta, "share": needle.MaxShare}, "", "  ")
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
	spec, err := needle.ReadSpec(filepath.Join(from, v.Name+".json"), false)
	if err != nil {
		return err
	}
	// The ignored regions are a person's and stay, whatever else the old spec says or lacks - they
	// outlive a change of the spec's fields; the rest of it is the old run's.
	var old struct {
		Ignore []needle.Rect `json:"ignore"`
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
	spec.Approved = &needle.Approval{Build: l.Version, OS: l.OS, New: v.New}
	if !v.New {
		share := v.Share
		spec.Approved.Share = &share
	}
	if err := spec.Validate(true); err != nil {
		return fmt.Errorf("%s: %w", v.Name, err)
	}
	if err := os.WriteFile(filepath.Join(needles, v.Name+".png"), b, 0o644); err != nil {
		return err
	}
	return spec.Write(filepath.Join(needles, v.Name+".json"))
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
	fmt.Fprintf(&b, "Build `%s`, against needles `%s`.\n\n| View | Differs | lf | Noise |\n|---|---|---|---|\n", l.Version, l.Needles)
	for _, v := range views {
		state := fmt.Sprintf("%.3f%%", v.Share*100)
		switch {
		case v.New:
			state = "new view"
		case v.Error != "":
			state = v.Error
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", v.Name, state, lfCell(v.LF), noiseCell(v.Noise, v.NeedleNoise))
	}
	return b.String()
}

// noiseCell is a view's noise, and how far it moved from its needle's when the needle has one.
func noiseCell(n needle.Noise, was *needle.Noise) string {
	s := fmt.Sprintf("edges %.2f%%, %d colours", n.Edges*100, n.PaletteColors)
	if was != nil {
		s += fmt.Sprintf(" (%+.2f, %+d)", (n.Edges-was.Edges)*100, n.PaletteColors-was.PaletteColors)
	}
	return s
}

// lfCell is lf's word on a view for the table: what failed, how many passed, or that it measured
// nothing but the sweep, or nothing at all.
func lfCell(r needle.LF) string {
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
