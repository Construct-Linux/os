package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// needleSpec is a view's <name>.json beside its <name>.png. Ignore is written by hand: regions a
// view's own content changes in. Actors is measured when the view is shot (a run writes it beside
// its screenshot, needles-propose copies it to the needle): where the clock, Vitals' readings,
// today's date and the lock clock were, which move with their text's width, so no fixed rectangle
// holds them. A comparison leaves out the needle's and the run's both, so text that grew or
// shrank between them is left out where it was in either.
type needleSpec struct {
	Ignore []needleRect `json:"ignore,omitempty"`
	Actors []needleRect `json:"actors,omitempty"`
}

// needleBarFallback is what is left out of a view whose actors could not be measured: the top
// bar's inside from the middle on, its ring still compared. At the harness's 1280x800, the bar
// from y 6 to 41.
var needleBarFallback = needleRect{X: 640, Y: 8, W: 634, H: 32, Why: "the top bar's readings, not measured"}

// needleActors is the JavaScript that measures, in the running shell, what changes from one run to
// the next: inside the top bar, its clock and Vitals' readings, kept inside the bar's ring so the
// ring and the bar's surface around them are compared; with the calendar open, today's date and
// the month's days; on the lock screen, its clock. Each grows by 2 px for antialiasing. No
// apostrophe in what it answers: gdbus would quote it otherwise, and guest.eval reads single quotes.
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
    add(dm._date, 'the date of today');
    add(dm._calendar, 'the days of the month');
}
add(Main.screenShield?._dialog?._clock, 'the lock clock');
JSON.stringify(out)`

// measureActors is what needleActors finds on the screen now.
func measureActors(g *guest) ([]needleRect, error) {
	raw, err := g.eval(needleActors)
	if err != nil {
		return nil, err
	}
	var inner string
	var rects []needleRect
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		return nil, fmt.Errorf("eval answered %.80q: %w", raw, err)
	}
	if err := json.Unmarshal([]byte(inner), &rects); err != nil {
		return nil, err
	}
	return rects, nil
}

// shotLook saves the screen as name.png, as shot does, and beside it name.json with where the
// actors whose content changes from run to run were, measured before and after the screenshot: the
// clock may tick between them. A view whose actors cannot be measured leaves out the top bar's
// inside instead, and fails a check that says so.
func (r *report) shotLook(v *vm, g *guest, name string) {
	before, errBefore := measureActors(g)
	r.shot(v, name)
	after, errAfter := measureActors(g)
	spec := needleSpec{Actors: append(before, after...)}
	if err := errors.Join(errBefore, errAfter); err != nil {
		spec.Actors = append(spec.Actors, needleBarFallback)
		r.add("look: "+name+" actors", false, "the clock and Vitals could not be measured: "+oneLine(err.Error()))
	}
	if b, err := json.MarshalIndent(spec, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(r.dir, name+".json"), b, 0o644)
	}
}

// readSpec reads a needleSpec; a view without one has nothing left out.
func readSpec(p string) (needleSpec, error) {
	var spec needleSpec
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return spec, nil
	}
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
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

// look is a run's comparison, _output/test/look.json: which build, against which needles, and
// each view - what needles-propose turns into a pull request in the needles repository.
type look struct {
	Version string     `json:"version"`
	Needles string     `json:"needles_commit"`
	Views   []lookView `json:"views"`
}

type lookView struct {
	Name  string  `json:"name"`
	Share float64 `json:"share"`
	New   bool    `json:"new,omitempty"`
	OK    bool    `json:"ok"`
	Error string  `json:"error,omitempty"`
}

// compareNeedles compares every screenshot in dir that has a needle in needles, writes each
// difference to dir/diff/<name>.png and the side by side to dir/look.html, and reports a check
// per view. Screenshots without a needle are listed as new.
func (r *report) compareNeedles(needles string) {
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
	l := look{Version: builtVersion(), Needles: gitHead(needles)}
	for _, x := range results {
		v := lookView{Name: x.name, Share: x.share, OK: x.err == nil && x.share <= needleMaxShare}
		if x.err != nil {
			v.Error = x.err.Error()
		}
		l.Views = append(l.Views, v)
	}
	for _, n := range unmatched {
		l.Views = append(l.Views, lookView{Name: n, New: true})
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
	spec, err := readSpec(specPath)
	if err != nil {
		return 0, err
	}
	run, err := readSpec(strings.TrimSuffix(shot, ".png") + ".json")
	if err != nil {
		return 0, err
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
// proposes nothing. The branch is look/<version>, from the needles' main.
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
		if !v.OK {
			changed = append(changed, v)
		}
	}
	if len(changed) == 0 {
		fmt.Println("every view matches its needle: nothing to propose")
		return nil
	}
	if l.Version == "" {
		return errors.New("needles-propose: look.json names no build")
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
	branch := "look/" + l.Version
	if err := git("fetch", "-q", "origin"); err != nil {
		return err
	}
	if err := git("checkout", "-q", "-B", branch, "origin/main"); err != nil {
		return err
	}
	// Back on main whatever happens: the checkout is where the next run compares from.
	defer func() { _ = git("checkout", "-q", "main") }()
	var body strings.Builder
	fmt.Fprintf(&body, "Build `%s`, against needles `%s`.\n\n| View | Differs |\n|---|---|\n", l.Version, l.Needles)
	for _, v := range changed {
		b, err := os.ReadFile(filepath.Join(*from, v.Name+".png"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*needles, v.Name+".png"), b, 0o644); err != nil {
			return err
		}
		if err := git("add", v.Name+".png"); err != nil {
			return err
		}
		// The needle keeps its hand-written regions and takes where this run's actors were.
		spec, err := readSpec(filepath.Join(*needles, v.Name+".json"))
		if err != nil {
			return err
		}
		run, err := readSpec(filepath.Join(*from, v.Name+".json"))
		if err != nil {
			return err
		}
		spec.Actors = run.Actors
		if len(spec.Ignore)+len(spec.Actors) > 0 {
			b, err := json.MarshalIndent(spec, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(*needles, v.Name+".json"), b, 0o644); err != nil {
				return err
			}
			if err := git("add", v.Name+".json"); err != nil {
				return err
			}
		}
		state := fmt.Sprintf("%.3f%%", v.Share*100)
		switch {
		case v.New:
			state = "new view"
		case v.Error != "":
			state = v.Error
		}
		fmt.Fprintf(&body, "| `%s` | %s |\n", v.Name, state)
	}
	body.WriteString("\nMerging approves these as how the views must look.\n")
	title := fmt.Sprintf("Look of %s: %d view(s)", l.Version, len(changed))
	if err := git("commit", "-q", "-m", title, "-m", body.String()); err != nil {
		return err
	}
	if err := git("push", "-q", "-f", "origin", branch); err != nil {
		return err
	}
	pr := exec.Command("gh", "pr", "create", "--title", title, "--body", body.String(), "--head", branch, "--base", "main")
	pr.Dir = *needles
	pr.Stdout, pr.Stderr = os.Stdout, os.Stderr
	return pr.Run()
}
