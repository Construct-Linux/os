package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/css"
	"github.com/Construct-Linux/os/internal/needle"
	"github.com/Construct-Linux/os/internal/pixels"
	"github.com/Construct-Linux/os/internal/qemu"
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
	// lfBlankShare is the share of one colour past which a screenshot drew nothing: the most
	// uniform view drawn, Text Editor maximized, is 92% its surface (test:usb, 2026-10-05).
	lfBlankShare = 0.98
	// How far either side of an extent's edge the ring is looked for.
	lfRingReach = 2
	// Text and tinted fills, against their tokens: glyph hinting and alpha rounding.
	lfTextTol = 4
	// The accent, counted: what is within this of it on every channel reads as the accent, the
	// tints (16-28%) are far outside it.
	lfAccentTol = 14
	// The calendar's today ring is 2 px on a curve: its pixels are half blends of the accent.
	lfRingAccentTol = 40
	// A quick toggle's title on its tint, and the focus ring's inset shadow over a toggle: thin
	// accent strokes on a fill, so, like the today ring, most of their pixels are blends of it.
	lfAccentTitleTol = 40
	lfFocusRingTol   = 40
	// The ring against what is 6 px outside it: the brand's minimum edge contrast.
	lfEdgeContrast = 1.5
	lfEdgeGap      = 6
	// WCAG 2.2 AA for body text.
	lfTextContrast = 4.5
	// Radii, in px of St's computed style.
	lfRadiusTol = 1
	// Concentric radii (THEMING): quick settings is its toggles' 22 plus its 18 of padding; the
	// overview's search section a control's radius plus its 12 of padding.
	lfQuickSettingsRadius  = 40
	lfSearchSectionPadding = 12
	// Cyan pixels a view may show: filled toggles and today put 12,982 and 862 there, measured;
	// tinted, only the text and the marks are the accent.
	lfAccentQuickSettings = 2500
	lfAccentCalendar      = 450
	// A focused day's ring: at least this share of its outline is the accent. Today draws none:
	// under this share, which a bold accent number crossing the outline stays well below.
	lfDayFocusRing = 0.60
	lfTodayNoRing  = 0.20
	// The quick toggle's disc: 24 px (THEMING's largest mark), and at least this share of its box
	// the accent: a disc is 0.785 of its box, less the glyph in on_accent and the antialiased rim
	// (measured 0.45 on the Dark Style toggle); an icon with no disc stays under 0.1.
	lfDiscSize  = 24
	lfDiscShare = 0.35
	// A mark is at most 4 px of stroke or a 24 px disc (THEMING, the accent scale): a solid accent
	// area thicker and larger than that is a primary action, and a view has at most one.
	lfMarkStroke     = 4
	lfMarkDisc       = 24
	lfAccentAreasMax = 1
	// The accent as the sweep counts it: a fill of it is drawn exact, and Tela's teal in the dock's
	// icons (#008292, 13 off light's accent) is imagery, not the accent.
	lfSweepTol = 4
	// The full accent's share of a view's pixels: tinted toggles and today keep every view under
	// it (round 2's sweep: solid toggles were 1.29% of quick settings, Clocks 1.12%, Users 0.95%).
	lfAccentShareMax = 0.009
	// The top bar's buttons: the spacing scale's step either side (Orchis eb033362); Activities, half
	// of it, is exempt (bars).
	lfPanelHPadding = 6
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
	// The overview's workspace card is the wallpaper drawn small: its mark sits at the share of
	// the card's height the wallpaper puts it at (wallpaper.py, W1), within lfCardMarkTol stage
	// pixels. The card's place on the screen changes with the screen's size; this does not.
	lfCardMarkShare = 0.4222
	lfCardMarkTol   = 6
	// Plymouth's mark against the card's: across, within lfCardMarkTol; down, within this, a
	// looser bound not yet measured.
	lfBootMarkDownTol = 12
	// The top bar's outer height, ring included.
	lfBarHeight = 36
	// Firefox's page of controls drew them in the accent: its checked box alone is ~14x14 px of
	// it, beside a radio, a range's fill and a progress bar at 60%. Fewer is a page not drawn.
	lfFirefoxAccentMin = 100
	// Adwaita's blue and the light accent as Firefox fills its controls: flat, so close; the
	// accent a little looser, its checked box's mark antialiased over it.
	lfFirefoxColorTol  = 8
	lfFirefoxAccentTol = 12
)

// Every pixel count, distance and coordinate above and in the checks is in the stage's pixels, as
// St reports extents, lengths and radii (logical: mutter lays monitors out in them). A screenshot
// is in the panel's physical pixels, k of them a stage pixel: lengths are taken k times, areas k²
// times, and an extent is l.px of it.

// lfPalette is construct-brand's palette.json as the guest has it: colours by token, the numbers
// (tints, the inset, the radii) and the type families.
type lfPalette struct {
	colors map[string]color.NRGBA
	nums   map[string]float64
	strs   map[string]string
}

// The tokens the checks read: a palette without one fails at once, rather than as black.
var (
	lfColorTokens = []string{"brand-boot", "neutral-a", "neutral-e", "neutral-f", "neutral-g", "neutral-h", "dark-border",
		"dark-accent", "dark-accent-text", "dark-text", "dark-muted", "dark-on-accent", "light-accent", "light-accent-text",
		"light-on-accent", "light-text", "light-muted", "light-desk", "light-shelf"}
	lfNumTokens = []string{"tint-dark-rest", "tint-dark-hover", "tint-dark-active", "line-light-ring", "line-light-border", "shape-inset", "shape-radius-control",
		"shape-radius-surface", "shape-radius-sheet"}
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
			if c, ok := pixels.ParseHex(s); ok && strings.HasPrefix(s, "#") {
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

// lfInks are what C1 lets a theme write at an alpha over a palette colour: white, black and the
// accent, and on_accent over the accent (the solid primary's hover and active).
var lfInks = []string{"dark-accent", "light-accent", "dark-on-accent", "light-on-accent"}

// composite is whether an opaque c is one of lfInks, white or black at some alpha over a palette
// colour, to a channel of rounding: what a theme writes where St cannot stack two backgrounds.
// The terminal's colours are no ground for it, and a grey is never one: the palette's surfaces
// carry the ink's hue, and a grey of its own is what no state is.
func (p lfPalette) composite(c color.NRGBA) bool {
	inks := []color.NRGBA{{0, 0, 0, 255}, {255, 255, 255, 255}}
	for _, k := range lfInks {
		inks = append(inks, p.colors[k])
	}
	grey := func(c color.NRGBA) bool { return c.R == c.G && c.G == c.B }
	if grey(c) {
		return false
	}
	for k, ground := range p.colors {
		if strings.HasPrefix(k, "ansi-") || strings.HasPrefix(k, "term-") {
			continue
		}
		for _, ink := range inks {
			if grey(ink) && grey(ground) {
				continue
			}
			if a, res := pixels.CoverOf(c, ink, ground); a > 0 && res <= 1 {
				return true
			}
		}
	}
	return false
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
            // St gives every widget that is not reactive :insensitive (st_widget_constructed), labels
            // and boxes included: only a control that cannot be used is disabled.
            if (p.has_style_pseudo_class('insensitive') && (p instanceof St.Button || p.track_hover))
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

// seen is what js, a measurement of the running shell, answers, decoded as T; a measurement that
// fails is the check rule surface's failure, and its view's checks are not run.
func seen[T any](l *lf, rule, surface, js string) (T, bool) {
	var s T
	if err := l.g.evalJSON(js, &s); err != nil {
		l.check(rule, surface, false, err.Error())
		return s, false
	}
	return s, true
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

func (l *lfLook) rect() pixels.Rect { return pixels.Rect{X: l.X, Y: l.Y, W: l.W, H: l.H} }

// surface is what the actor's own background composites to over its ancestors', from the bottom
// of the stage up: the colour its text is read against.
func (l *lfLook) surface(ground color.NRGBA) color.NRGBA {
	s := ground
	for _, h := range l.Chain {
		if c, ok := pixels.ParseHex(h); ok {
			s = pixels.Over(c, s)
		}
	}
	return s
}

// ink is the actor's foreground as drawn: its colour, at its alpha and its ancestors' opacity,
// over its surface.
func (l *lfLook) ink(ground color.NRGBA) color.NRGBA {
	fg, _ := pixels.ParseHex(l.FG)
	fg.A = uint8(math.Round(float64(fg.A) * l.Opacity))
	return pixels.Over(fg, l.surface(ground))
}

// lf carries a run's look-and-feel checks: the palette they measure against, and what one view
// measures for a check another view finishes.
type lf struct {
	r   *report
	v   *qemu.VM
	g   *guest
	pal lfPalette
	ok  bool
	// The menus' tops, by menu: quick settings and the calendar hang at the same height.
	menuY map[string]int
	// The overview card's mark centre in physical pixels, and Plymouth's as shares of the screen it
	// drew on, which must agree. A VM's kernel picks another mode than the session's (2560x1600
	// under 2880x1800); a laptop's panel has one.
	cardMark, bootMark *[2]float64
	// The stage's size, and k, the physical pixels a stage pixel is in the screenshot last read.
	stageW, stageH, k int
	// cur is the view the checks now measure, whose spec counts them; none for the checks on the
	// guest and across views.
	cur string
	// lockError is the lock prompt as measured before shell-lock-error was shot, for its view.
	lockError *lockErrorSeen
}

// newLF reads the guest's palette.json; without it no lf check can be measured, and one failure
// says why.
func newLF(r *report, v *qemu.VM, g *guest) *lf {
	l := &lf{r: r, v: v, g: g, menuY: map[string]int{}, k: 1}
	var st [2]int
	if err := g.evalJSON(`return [global.stage.width, global.stage.height];`, &st); err != nil {
		r.add("lf: stage", false, "the stage's size: "+err.Error())
		return l
	}
	l.stageW, l.stageH = st[0], st[1]
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
	r.pal = &l.pal
	return l
}

func (l *lf) check(rule, surface string, ok bool, detail string) {
	l.r.add("lf: "+rule+" "+surface, ok, detail)
	l.r.viewCheck(l.cur, rule+" "+surface, ok)
}

func (l *lf) c(token string) color.NRGBA { return l.pal.colors[token] }

// img is a view's screenshot, as r.shot saved it; it sets l.k from its width over the stage's.
func (l *lf) img(name string) (image.Image, bool) {
	img, err := pixels.ReadPNG(filepath.Join(l.r.dir, name+".png"))
	if err != nil {
		l.check("screenshot", name, false, err.Error())
		return nil, false
	}
	k, ok := scaleOf(img.Bounds().Dx(), l.stageW)
	if !ok {
		l.check("screenshot", name, false, fmt.Sprintf("%d px wide over a %d px stage: no whole scale to measure at", img.Bounds().Dx(), l.stageW))
		return nil, false
	}
	l.k = k
	return img, true
}

// px is a stage rectangle in the screenshot's pixels.
func (l *lf) px(r pixels.Rect) pixels.Rect {
	return pixels.Rect{X: r.X * l.k, Y: r.Y * l.k, W: r.W * l.k, H: r.H * l.k}
}

// view runs the checks that measure the view just shot.
func (l *lf) view(name string) {
	if !l.ok {
		return
	}
	l.cur = name
	img, ok := l.img(name)
	if !ok {
		return
	}
	// A screen of one colour drew nothing - a display blanked, a window not yet painted - and
	// every check below that asks for what is not there (no accent, not Chrome's grey, the boot's
	// black) would pass on it.
	if c, share := pixels.ModeColor(img, pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}); share > lfBlankShare {
		l.check("drawn", name, false, fmt.Sprintf("%.1f%% of the screen is %s: nothing drawn to measure", share*100, pixels.Hex(c)))
		return
	}
	l.sweep(name, img)
	switch name {
	case "desktop":
		if s, ok := seen[barsSeen](l, "bars", name, lfBars); ok {
			l.bars(name, img, true, s)
		}
		n := pixels.CountNear(img, pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
		l.check("accent", "desktop", n == 0, fmt.Sprintf("%d px of the accent, want none", n))
	case "shell-settings-light":
		if s, ok := seen[barsSeen](l, "bars", name, lfBars); ok {
			l.bars(name, img, false, s)
		}
		if s, ok := seen[windowSeen](l, "radius", "settings-light window", lfSettingsWindow); ok {
			l.settingsLight(img, s)
		}
	case "shell-quick-settings":
		if s, ok := seen[quickSettingsSeen](l, "quick settings", "eval", lfQuickSettingsToggles); ok {
			l.quickSettings(img, s)
		}
	case "shell-quick-settings-focus":
		if s, ok := seen[focusSeen](l, "focus", "quick toggle", lfFocusToggle); ok {
			l.focus(img, s)
		}
	case "shell-quick-settings-submenu":
		if s, ok := seen[submenuSeen](l, "submenu", "quick settings", lfSubmenu); ok {
			l.submenu(img, s)
		}
	case "shell-calendar":
		if s, ok := seen[calendarSeen](l, "calendar", "eval", lfCalendar); ok {
			l.calendar(img, s)
		}
	case "shell-calendar-focus":
		if s, ok := seen[dayFocusSeen](l, "focus", "calendar day", lfDayFocus); ok {
			l.dayFocus(img, s)
		}
	case "shell-overview":
		l.ground(name, img)
		if s, ok := seen[overviewSeen](l, "ground", "overview", lfOverview); ok {
			l.overview(img, s)
		}
	case "shell-overview-light":
		l.ground(name, img)
		if s, ok := seen[overviewSeen](l, "ground", "overview-light", lfOverview); ok {
			l.overviewLight(img, s)
		}
	case "shell-overview-search":
		if s, ok := seen[searchSeen](l, "search", "overview", lfSearch); ok {
			l.search(img, s)
		}
	case "shell-app-grid":
		l.ground(name, img)
	case "shell-lock-error":
		if l.lockError != nil {
			l.lockErrorCheck(*l.lockError)
		}
	case "shell-lock-screen":
		if s, ok := seen[lockSeen](l, "lock", "eval", lfLock); ok {
			l.lock(img, s)
		}
	case "installer":
		if s, ok := seen[windowSeen](l, "window", "installer", lfFocusWindow); ok {
			l.installer(img, s)
		}
	case "gtk-popover-light", "gtk-tooltip-light", "gtk-dialog-light", "gtk-maximized-light", "gtk-banner-light", "gtk-destructive-light":
		r, err := l.appWindow("org.gnome.TextEditor.desktop")
		if err != nil {
			l.check("window", name, false, err.Error())
			return
		}
		l.gtkLight(name, img, r)
	case "gtk-focus", "gtk-focus-light":
		l.gtkFocus(name, img)
	case "shell-states":
		if s, ok := seen[[]stateSeen](l, "states", "quick toggles", lfStates); ok {
			l.states(img, s)
		}
	default:
		// E2: both bars' edge over every wallpaper. At another scale St's extents are not the
		// screenshot's pixels.
		if strings.HasPrefix(name, "desktop-") && name != lfScaledDesktop {
			if s, ok := seen[barsSeen](l, "bars", name, lfBars); ok {
				l.bars(name, img, false, s)
			}
		}
	}
}

// lfScaledDesktop is the desktop at 150%, whose screenshot is in physical pixels and St's extents
// in logical ones: its needle is its check.
const lfScaledDesktop = "desktop-scale-150"

// sweep is C3 on any view: at most one solid accent area larger than a mark (the one primary
// action), and the full accent - dark's and light's - no more than lfAccentShareMax of it.
func (l *lf) sweep(name string, img image.Image) {
	cols := []color.NRGBA{l.c("dark-accent"), l.c("light-accent")}
	all := pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}
	areas := pixels.SolidAreas(img, all, cols, lfSweepTol, lfMarkStroke*l.k, lfMarkDisc*l.k)
	var where []string
	for _, a := range areas {
		where = append(where, a.String())
	}
	l.check("accent areas", name, len(areas) <= lfAccentAreasMax,
		fmt.Sprintf("%d solid accent areas larger than a mark, want <= %d: %v", len(areas), lfAccentAreasMax, where))
	share := pixels.Share(img, all, cols, lfSweepTol)
	l.check("accent share", name, share <= lfAccentShareMax,
		fmt.Sprintf("%.2f%% of the view the full accent, want <= %.1f%%", share*100, lfAccentShareMax*100))
	l.r.viewSweep(name, areas, share)
}

// lfSweepChecks is how many checks the sweep makes on every view: a view with no more than these
// is one lf measures for C3 alone.
const lfSweepChecks = 2

// surfaceCheck is S1-S3 for one surface: St's background is want and opaque, the pixels inside its
// extents are want, its ring is 1 px of the border colour all round in St and on the screen, and
// the ring stands out from what is 6 px outside it.
func (l *lf) surfaceCheck(name string, a *lfLook, img image.Image, want color.NRGBA) {
	if a == nil || !a.Mapped || a.W <= 0 || a.H <= 0 {
		l.check("surface", name, false, "not on the screen")
		return
	}
	bg, _ := pixels.ParseHex(a.BG)
	r := l.px(a.rect())
	k := l.k
	// Inside the ring and clear of the corners' curve.
	rad := max(min(a.H/2, maxRadius(a)), 3) * k
	inner := pixels.Rect{X: r.X + rad, Y: r.Y + 3*k, W: r.W - 2*rad, H: r.H - 6*k}
	mode, share := pixels.ModeColor(img, inner)
	l.check("surface", name, bg == color.NRGBA{want.R, want.G, want.B, 255} && pixels.Near(mode, want, lfSurfaceTol),
		fmt.Sprintf("St %s, drawn %s over %.0f%% of %v; want %s opaque", a.BG, pixels.Hex(mode), share*100, inner, pixels.Hex(want)))
	ring := l.c("dark-border")
	stRing := len(a.BW) == 4 && len(a.BC) == 4
	for i := range 4 {
		if !stRing {
			break
		}
		c, _ := pixels.ParseHex(a.BC[i])
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
		p, hit := pixels.RingHit(img, e.x, e.y, e.nx, e.ny, lfRingReach*k, ring)
		if !hit {
			missed = append(missed, fmt.Sprintf("%s %s", e.name, pixels.Hex(pixels.At(img, e.x, e.y))))
			continue
		}
		o := image.Pt(p.X+e.nx*lfEdgeGap*k, p.Y+e.ny*lfEdgeGap*k)
		if !o.In(img.Bounds()) {
			continue
		}
		if c := pixels.Contrast(pixels.At(img, p.X, p.Y), pixels.At(img, o.X, o.Y)); c < lfEdgeContrast {
			weak = append(weak, fmt.Sprintf("%s %.2f against %s", e.name, c, pixels.Hex(pixels.At(img, o.X, o.Y))))
		}
	}
	l.check("ring", name, stRing && len(missed) == 0,
		fmt.Sprintf("St %v px of %v; edges without %s: %v", a.BW, a.BC, pixels.Hex(ring), missed))
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

// barsSeen is the top bar, the dock, a popup menu and a banner as St has them, the top bar's labels
// and its buttons' padding.
type barsSeen struct {
	Panel, Dock, Menu, Banner *lfLook
	Labels                    []*lfLook
	Pads                      []float64
}

const lfBars = `
		const menu = Object.values(Main.panel.statusArea).map(i => i.menu?.box).find(b => b instanceof St.Widget &&
			b.has_style_class_name('popup-menu-content') && !b.has_style_class_name('quick-settings') && !b.has_style_class_name('datemenu-popover'));
		// A banner exists only while a notification shows: one like it, styled as the theme styles it.
		const banner = new St.Bin({style_class: 'message notification-banner'});
		Main.uiGroup.add_child(banner);
		const b = look(banner);
		banner.destroy();
		// The clock's button has no padding of its own: its label carries it. Activities is narrower
		// on purpose: Orchis gives it half the step (%icon_activities, $space-size / 2), so its pill
		// of workspace dots sits as close to the bar's end as the bar's own inset.
		const activities = Main.panel.statusArea.activities;
		const pads = lfAll(Main.panel, lfClass('panel-button')).filter(a => a.is_mapped() && !a.has_style_class_name('clock-display') &&
			a !== activities && a !== activities?.container && a.name !== 'panelActivities')
			.map(a => a.get_theme_node().get_length('-natural-hpadding'));
		return {panel: look(Main.panel), dock: look(lfDock()), menu: look(menu), banner: b, labels: lfLabels(Main.panel), pads};`

// bars checks the top bar and the dock, S1-S3: opaque g with a ring, in the dark desktop and in
// the light scheme alike (the shell is dark in both). On the desktop it checks their shape and
// placement too, and the text of the top bar.
func (l *lf) bars(view string, img image.Image, desktop bool, s barsSeen) {
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
	gap := img.Bounds().Dy()/l.k - (s.Dock.Y + s.Dock.H)
	l.check("spacing", "bars", s.Panel.Y == inset && abs(gap-inset) <= 1 && s.Panel.H == lfBarHeight,
		fmt.Sprintf("top bar at y %d, %d px high; dock %d px off the bottom; want %d, %d and %d", s.Panel.Y, s.Panel.H, gap, inset, lfBarHeight, inset))
	var offPad []float64
	for _, p := range s.Pads {
		if math.Abs(p-lfPanelHPadding) > 0.5 {
			offPad = append(offPad, p)
		}
	}
	l.check("spacing", "top bar buttons", len(s.Pads) > 0 && len(offPad) == 0,
		fmt.Sprintf("%d buttons besides Activities; -natural-hpadding not %d px: %v", len(s.Pads), lfPanelHPadding, offPad))
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
		if c := pixels.Contrast(t.ink(ground), t.surface(ground)); c < lfTextContrast {
			low = append(low, fmt.Sprintf("%q %s on %s %.2f", t.Text, pixels.Hex(t.ink(ground)), pixels.Hex(t.surface(ground)), c))
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
	Icon     *lfLook `json:"icon"`
}

const lfQuickSettings = `const m = Main.panel.statusArea.quickSettings.menu;`

// quickSettingsSeen is quick settings' menu, its toggles and its labels.
type quickSettingsSeen struct {
	Box     *lfLook
	Toggles []quickToggle
	Labels  []*lfLook
}

const lfQuickSettingsToggles = lfQuickSettings + `
	const toggles = lfAll(m.box, lfClass('quick-toggle')).filter(a => a.is_mapped()).map(a => ({
		look: look(a), checked: a.checked, title: look(a._title), subtitle: a._subtitle?.visible ? look(a._subtitle) : null,
		icon: look(find(a, 'quick-toggle-icon'))}));
	return {box: look(m.box), toggles, labels: lfLabels(m.box)};`

func (l *lf) quickSettings(img image.Image, s quickSettingsSeen) {
	h := l.c("neutral-h")
	l.surfaceCheck("quick settings", s.Box, img, h)
	l.radiusCheck("quick settings", s.Box, lfQuickSettingsRadius)
	if s.Box != nil {
		l.menuY["quick settings"] = s.Box.Y
	}
	n := pixels.CountNear(img, pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
	most := lfAccentQuickSettings * l.k * l.k
	l.check("accent", "quick settings", n < most, fmt.Sprintf("%d px of the accent, want under %d", n, most))
	fill := pixels.Tint(l.c("dark-accent"), h, l.pal.nums["tint-dark-rest"])
	ground := l.c("neutral-e")
	var on, off int
	var bad, discs []string
	minH, maxH := math.MaxInt, 0
	for _, t := range s.Toggles {
		if t.Look == nil || t.Title == nil {
			continue
		}
		minH, maxH = min(minH, t.Look.H), max(maxH, t.Look.H)
		discs = append(discs, l.disc(img, t)...)
		if t.Checked {
			on++
			mode, _ := pixels.ModeColor(img, l.px(t.Look.rect()).Inset(4*l.k, 4*l.k))
			if !pixels.Near(mode, fill, lfTextTol) {
				bad = append(bad, fmt.Sprintf("%q on is %s, not the tint %s", t.Title.Text, pixels.Hex(mode), pixels.Hex(fill)))
			}
			if c := t.Title.ink(ground); !pixels.Near(c, l.c("dark-accent-text"), lfTextTol) || pixels.CountNear(img, l.px(t.Title.rect()), l.c("dark-accent-text"), lfAccentTitleTol) == 0 {
				bad = append(bad, fmt.Sprintf("%q on titled %s, not the accent", t.Title.Text, pixels.Hex(c)))
			}
			if t.Subtitle != nil && !pixels.Near(t.Subtitle.ink(ground), l.c("dark-text"), lfTextTol) {
				bad = append(bad, fmt.Sprintf("%q on subtitled %s, not text: muted never goes on a tint", t.Title.Text, pixels.Hex(t.Subtitle.ink(ground))))
			}
			continue
		}
		off++
		if c := t.Title.ink(ground); !pixels.Near(c, l.c("dark-text"), lfTextTol) {
			bad = append(bad, fmt.Sprintf("%q off titled %s, not text", t.Title.Text, pixels.Hex(c)))
		}
		if t.Subtitle != nil && !pixels.Near(t.Subtitle.ink(ground), l.c("dark-muted"), lfTextTol) {
			bad = append(bad, fmt.Sprintf("%q off subtitled %s, not muted", t.Title.Text, pixels.Hex(t.Subtitle.ink(ground))))
		}
	}
	l.check("toggles", "quick settings", on > 0 && off > 0 && len(bad) == 0, fmt.Sprintf("%d on, %d off: %v", on, off, bad))
	// The disc must not grow the toggle it sits in: every toggle is one height, on or off.
	l.check("disc", "quick toggles", on > 0 && len(discs) == 0 && maxH-minH <= 1,
		fmt.Sprintf("%d on; heights %d to %d px; %v", on, minH, maxH, discs))
	l.labelsCheck("quick settings", s.Labels)
}

// disc is what is wrong with a quick toggle's icon: on, it sits on a 24 px accent disc, drawn;
// off, on nothing.
func (l *lf) disc(img image.Image, t quickToggle) []string {
	if t.Icon == nil || t.Icon.W == 0 {
		return []string{fmt.Sprintf("%q has no icon", t.Title.Text)}
	}
	bg, _ := pixels.ParseHex(t.Icon.BG)
	accent := l.c("dark-accent")
	if !t.Checked {
		if bg.A != 0 {
			return []string{fmt.Sprintf("%q off has an icon ground %s", t.Title.Text, t.Icon.BG)}
		}
		return nil
	}
	var bad []string
	if bg != (color.NRGBA{accent.R, accent.G, accent.B, 255}) {
		bad = append(bad, fmt.Sprintf("%q on: icon ground %s, not %s", t.Title.Text, t.Icon.BG, pixels.Hex(accent)))
	}
	if abs(t.Icon.W-lfDiscSize) > 1 || abs(t.Icon.H-lfDiscSize) > 1 || maxRadius(t.Icon)*2 < t.Icon.H {
		bad = append(bad, fmt.Sprintf("%q on: icon %dx%d radius %v, not a %d px disc", t.Title.Text, t.Icon.W, t.Icon.H, t.Icon.Radius, lfDiscSize))
	}
	r := l.px(t.Icon.rect())
	if share := float64(pixels.CountNear(img, r, accent, lfAccentTol)) / float64(max(1, r.W*r.H)); share < lfDiscShare {
		bad = append(bad, fmt.Sprintf("%q on: %.0f%% of its icon drawn the accent, want >= %.0f%%", t.Title.Text, share*100, lfDiscShare*100))
	}
	return bad
}

// focusSeen is the first quick toggle, and whether it has the key focus.
type focusSeen struct {
	Toggle  *lfLook
	Focused bool
}

const lfFocusToggle = lfQuickSettings + `
	const t = find(m.box, 'quick-toggle');
	return {toggle: look(t), focused: t !== null && global.stage.key_focus === t};`

// focus is S8: the first quick toggle, given the key focus, shows the accent's inset ring, and the
// ring is what focus changed on the screen.
func (l *lf) focus(img image.Image, s focusSeen) {
	if s.Toggle == nil || !s.Focused {
		l.check("focus", "quick toggle", false, fmt.Sprintf("the first quick toggle did not take the key focus (found: %v)", s.Toggle != nil))
		return
	}
	accent := l.c("dark-accent")
	shOK, detail := focusRing(s.Toggle.Shadow, accent)
	l.check("focus", "quick toggle style", shOK, detail+"; want inset, spread >= 2, "+pixels.Hex(accent))
	before, ok := l.img("shell-quick-settings")
	if !ok {
		return
	}
	r := l.px(s.Toggle.rect()).Inset(-2*l.k, -2*l.k)
	drawn, share := pixels.FocusDiff(before, img, r, needle.ChannelDelta, accent, lfFocusRingTol)
	least := lfFocusMinChanged * l.k * l.k
	l.check("focus", "quick toggle drawn", drawn >= least && share >= lfFocusAccentShare,
		fmt.Sprintf("%d px of the accent drawn in %v against shell-quick-settings, %.0f%% of the change the accent; want >= %d and >= %.0f%%",
			drawn, r, share*100, least, lfFocusAccentShare*100))
}

// submenuSeen is quick settings' box and the open toggle menu's.
type submenuSeen struct{ Box, Well *lfLook }

const lfSubmenu = lfQuickSettings + `
	const t = lfAll(m.box, lfClass('quick-toggle-has-menu')).find(t => t.menu?.isOpen);
	return {box: look(m.box), well: look(t?.menu.box ?? null)};`

// submenu is the expanded submenu: a g well in the h menu, with no light slab in it. A toggle's
// menu is not under the menu's box: QuickSettingsMenu lays them in an overlay of its own.
func (l *lf) submenu(img image.Image, s submenuSeen) {
	if s.Box == nil {
		l.check("submenu", "quick settings", false, "no quick settings box")
		return
	}
	if s.Well == nil || !s.Well.Mapped || s.Well.W == 0 {
		l.check("submenu", "quick settings", false, "no toggle's menu is open")
		return
	}
	g := l.c("neutral-g")
	bg, _ := pixels.ParseHex(s.Well.BG)
	rad := maxRadius(s.Well)
	mode, share := pixels.ModeColor(img, l.px(s.Well.rect()).Inset(max(rad, 3)*l.k, max(rad/2, 3)*l.k))
	area := pixels.LightSlab(img, l.px(s.Box.rect()), lfSlabLightness)
	most := lfSlabMaxArea * l.k * l.k
	l.check("submenu", "quick settings", bg == color.NRGBA{g.R, g.G, g.B, 255} && pixels.Near(mode, g, lfSurfaceTol) && area <= most,
		fmt.Sprintf("well St %s, drawn %s over %.0f%% (want %s); largest area lighter than %.0f%%: %d px, want <= %d",
			s.Well.BG, pixels.Hex(mode), share*100, pixels.Hex(g), lfSlabLightness*100, area, most))
}

// calendarDay is a day of the month's grid as the checks see it.
type calendarDay struct {
	Look    *lfLook `json:"look"`
	Today   bool    `json:"today"`
	Other   bool    `json:"other"`
	Weekend bool    `json:"weekend"`
	Label   string  `json:"label"`
}

// lfDays are the date menu's day buttons, each with what calendar.js marks it.
const lfDays = `const dm = Main.panel.statusArea.dateMenu;
	const days = (dm._calendar._buttons ?? []).filter(b => b.is_mapped() && b.has_style_class_name('calendar-day'));`

// calendarSeen is the date menu: its box, its heading, the month's days and its labels, and
// whether today is selected and has the key focus.
type calendarSeen struct {
	Box, Heading    *lfLook
	Selected, Focus bool
	Days            []calendarDay
	Labels          []*lfLook
}

const lfCalendar = lfDays + `
	const today = days.find(b => b.has_style_class_name('calendar-today'));
	return {box: look(dm.menu.box), selected: today?.has_style_pseudo_class('selected') ?? false,
		focus: today !== undefined && global.stage.key_focus === today, heading: look(dm._date?._dayLabel),
		days: days.map(b => ({look: look(b), today: b === today, other: b.has_style_class_name('calendar-other-month'),
			weekend: b.has_style_class_name('calendar-weekend'), label: b.label ?? ''})),
		labels: lfLabels(dm.menu.box)};`

func (l *lf) calendar(img image.Image, s calendarSeen) {
	h := l.c("neutral-h")
	l.surfaceCheck("calendar", s.Box, img, h)
	l.radiusCheck("calendar", s.Box, int(l.pal.nums["shape-radius-sheet"]))
	if s.Box != nil {
		l.menuY["calendar"] = s.Box.Y
	}
	n := pixels.CountNear(img, pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}, l.c("dark-accent"), lfAccentTol)
	most := lfAccentCalendar * l.k * l.k
	l.check("accent", "calendar", n < most, fmt.Sprintf("%d px of the accent, want under %d", n, most))
	l.today(img, s.Days, s.Selected, s.Focus)
	// F1 and Orchis eb033362: every day of the month is text, weekends too; only the other months'
	// days are muted, so the month's edge reads.
	ground := l.c("neutral-e")
	var off []string
	month := 0
	for _, d := range s.Days {
		if d.Look == nil || d.Today {
			continue
		}
		want := l.c("dark-text")
		if d.Other {
			want = l.c("dark-muted")
		} else {
			month++
		}
		if c := d.Look.ink(ground); !pixels.Near(c, want, lfTextTol) {
			off = append(off, fmt.Sprintf("%q (other month %v, weekend %v) %s, want %s", d.Label, d.Other, d.Weekend, pixels.Hex(c), pixels.Hex(want)))
		}
	}
	l.check("text", "calendar days", month > 0 && len(off) == 0, fmt.Sprintf("%d days of the month; off: %v", month, off))
	// The menu's title, today's weekday: primary text, as the date under it.
	if s.Heading == nil {
		l.check("text", "calendar heading", false, "no day label")
	} else {
		c := s.Heading.ink(ground)
		l.check("text", "calendar heading", pixels.Near(c, l.c("dark-text"), lfTextTol), fmt.Sprintf("%q %s, want %s", s.Heading.Text, pixels.Hex(c), pixels.Hex(l.c("dark-text"))))
	}
	l.labelsCheck("calendar", s.Labels)
	if q, ok := l.menuY["quick settings"]; ok && s.Box != nil {
		l.check("spacing", "menus", abs(q-s.Box.Y) <= 1, fmt.Sprintf("quick settings at y %d, the calendar at %d", q, s.Box.Y))
	}
}

// today is F1's today: accent_text at 700 with no ring and no box-shadow, unless it has the key
// focus; the menu opens on it, so it is the selected day too, the rest tint inside.
func (l *lf) today(img image.Image, days []calendarDay, selected, focus bool) {
	var t *lfLook
	for _, d := range days {
		if d.Today {
			t = d.Look
		}
	}
	if t == nil || t.W == 0 {
		l.check("today", "calendar", false, "no day styled calendar-today")
		return
	}
	h := l.c("neutral-h")
	centre := h
	if selected {
		centre = pixels.Tint(l.c("dark-accent"), h, l.pal.nums["tint-dark-rest"])
	}
	mode, _ := pixels.ModeColor(img, l.px(t.rect()).Inset(4*l.k, 4*l.k))
	share := pixels.RingShare(img, l.px(t.rect()), float64(l.k), l.c("dark-accent"), lfRingAccentTol)
	fg, _ := pixels.ParseHex(t.FG)
	accent := l.c("dark-accent-text")
	shadow := t.Shadow != nil && !strings.HasSuffix(t.Shadow.Color, "00")
	ok := pixels.Near(mode, centre, lfTextTol) && t.Weight == 700 && fg == color.NRGBA{accent.R, accent.G, accent.B, 255}
	if !focus {
		ok = ok && !shadow && share < lfTodayNoRing
	}
	l.check("today", "calendar", ok,
		fmt.Sprintf("centre %s, want %s (selected %v); %s %d, want %s 700; box-shadow %v, %.0f%% of its outline the accent, want none and under %.0f%% (focused %v)",
			pixels.Hex(mode), pixels.Hex(centre), selected, t.FG, t.Weight, pixels.Hex(accent), shadow, share*100, lfTodayNoRing*100, focus))
}

// focusRing is whether St's box-shadow is the key focus's ring - inset, 2 px or more, the accent
// opaque - and what it is.
func focusRing(sh *lfShadow, accent color.NRGBA) (bool, string) {
	if sh == nil {
		return false, "no box-shadow"
	}
	c, _ := pixels.ParseHex(sh.Color)
	return sh.Inset && sh.Spread >= 2 && c == color.NRGBA{accent.R, accent.G, accent.B, 255},
		fmt.Sprintf("box-shadow %s inset=%v spread %.0f", sh.Color, sh.Inset, sh.Spread)
}

// dayFocusSeen is the calendar day with the key focus, when it is not today.
type dayFocusSeen struct {
	Day     *lfLook
	Label   string
	Focused bool
}

const lfDayFocus = lfDays + `
	const f = global.stage.key_focus;
	return {day: days.includes(f) ? look(f) : null, label: days.includes(f) ? f.label ?? '' : '',
		focused: days.includes(f) && !f.has_style_class_name('calendar-today')};`

// dayFocus is F1 on a calendar day that is not today: with the key focus it draws the inset 2px
// accent ring, in St and on the screen.
func (l *lf) dayFocus(img image.Image, s dayFocusSeen) {
	if s.Day == nil || !s.Focused {
		l.check("focus", "calendar day", false, "no day but today has the key focus")
		return
	}
	accent := l.c("dark-accent")
	ok, detail := focusRing(s.Day.Shadow, accent)
	share := pixels.RingShare(img, l.px(s.Day.rect()), float64(l.k), accent, lfRingAccentTol)
	l.check("focus", "calendar day", ok && share >= lfDayFocusRing,
		fmt.Sprintf("%q: %s; %.0f%% of its outline the accent, want inset, spread >= 2, %s and >= %.0f%%", s.Label, detail, share*100, pixels.Hex(accent), lfDayFocusRing*100))
}

// ground is the overview's and the app grid's: the boot's black.
func (l *lf) ground(view string, img image.Image) {
	// Left of the overview's content and under the top bar, at any scale.
	x, y := 5*l.k, 100*l.k
	c := pixels.At(img, x, y)
	l.check("ground", view, pixels.Near(c, l.c("brand-boot"), lfSurfaceTol), fmt.Sprintf("(%d,%d) is %s, want %s", x, y, pixels.Hex(c), pixels.Hex(l.c("brand-boot"))))
}

// overviewSeen is the overview's group and the active workspace's card, as St has them.
type overviewSeen struct{ Group, Card *lfLook }

const lfOverview = `
		// The active workspace's card is the one most on the screen: the next one is as wide, mostly
		// past the right edge.
		const shown = a => {
			const b = lfBox(a);
			const w = Math.min(b.x + b.w, global.stage.width) - Math.max(b.x, 0);
			const h = Math.min(b.y + b.h, global.stage.height) - Math.max(b.y, 0);
			return Math.max(w, 0) * Math.max(h, 0);
		};
		const cards = lfAll(Main.layoutManager.overviewGroup, lfClass('workspace-background')).filter(a => a.is_mapped());
		cards.sort((a, b) => shown(b) - shown(a));
		return {group: look(Main.layoutManager.overviewGroup), card: look(cards[0])};`

func (l *lf) overview(img image.Image, s overviewSeen) {
	group, c := s.Group, s.Card
	if group != nil {
		l.check("ground", "overviewGroup", group.BG == pixels.Hexa(l.c("brand-boot")), "St "+group.BG)
	}
	if c == nil || c.W == 0 {
		l.check("mark", "overview card", false, "no workspace card on the screen")
		return
	}
	card := l.px(c.rect())
	box, ok := pixels.MarkBox(img, card.Inset(8*l.k, 8*l.k), markLevel)
	if !ok {
		l.check("mark", "overview card", false, "no mark in the card "+card.String())
		return
	}
	cx, cy := float64(box.X)+float64(box.W)/2, float64(box.Y)+float64(box.H)/2
	l.cardMark = &[2]float64{cx, cy}
	want, tol := float64(card.Y)+lfCardMarkShare*float64(card.H), float64(lfCardMarkTol*l.k)
	l.check("mark", "overview card", math.Abs(cy-want) <= tol,
		fmt.Sprintf("centred at (%.0f, %.0f), want y %.0f±%.0f", cx, cy, want, tol))
}

// lfCardLight is the least lightness of the light wallpaper's ground, drawn small in the card: the
// light desk is 0.9, the dark wallpapers' ink under 0.1, so half way tells them apart.
const lfCardLight = 0.5

// overviewLight is the overview in the light scheme: the shell's ground stays the boot's black
// (S4: the shell is dark in both), and the workspace card is the light wallpaper.
func (l *lf) overviewLight(img image.Image, s overviewSeen) {
	group, c := s.Group, s.Card
	if group != nil {
		l.check("ground", "overviewGroup light", group.BG == pixels.Hexa(l.c("brand-boot")), "St "+group.BG)
	}
	if c == nil || c.W == 0 {
		l.check("card", "overview-light", false, "no workspace card on the screen")
		return
	}
	mode, share := pixels.ModeColor(img, l.px(c.rect()).Inset(8*l.k, 8*l.k))
	l.check("card", "overview-light", pixels.Lightness(mode) >= lfCardLight,
		fmt.Sprintf("the card is %s over %.0f%% of it, lightness %.2f; want the light wallpaper, >= %.2f", pixels.Hex(mode), share*100, pixels.Lightness(mode), lfCardLight))
}

// searchSeen is the overview's search entry and its results' section.
type searchSeen struct{ Entry, Section *lfLook }

const lfSearch = `return {entry: look(Main.overview.searchEntry), section: look(find(Main.layoutManager.overviewGroup, 'search-section-content'))};`

func (l *lf) search(img image.Image, s searchSeen) {
	h := l.c("neutral-h")
	l.surfaceCheck("search entry", s.Entry, img, h)
	l.surfaceCheck("search section", s.Section, img, h)
	// Concentric: a control's radius plus the section's padding.
	l.radiusCheck("search section", s.Section, int(l.pal.nums["shape-radius-control"])+lfSearchSectionPadding)
}

// lockSeen is the lock screen's group and clock, and the stylesheet the shell loaded.
type lockSeen struct {
	Group, Time *lfLook
	URI         string
}

const lfLock = `
	const css = St.ThemeContext.get_for_stage(global.stage).get_theme().default_stylesheet;
	return {group: look(Main.screenShield._lockDialogGroup), time: look(Main.screenShield._dialog?._clock?._time), uri: css ? css.get_uri() : ''};`

func (l *lf) lock(img image.Image, s lockSeen) {
	if s.Group == nil {
		l.check("ground", "lock", false, "no lockDialogGroup")
	} else {
		l.check("ground", "lock", s.Group.BG == pixels.Hexa(l.c("brand-boot")), "St "+s.Group.BG)
	}
	const want = "resource:///org/gnome/shell/theme/gnome-shell-dark.css"
	l.check("theme", "lock", s.URI == want, fmt.Sprintf("the shell's default stylesheet is %q, want %s (orchis-theme-shell's gnome-shell-theme.gresource)", s.URI, want))
	if s.Time == nil || s.Time.W == 0 {
		l.check("type", "lock clock", false, "no clock on the lock screen")
		return
	}
	fg, _ := pixels.ParseHex(s.Time.FG)
	// The label's top rows are its padding, clear of the digits.
	bg, _ := pixels.ModeColor(img, l.px(pixels.Rect{X: s.Time.X, Y: s.Time.Y, W: s.Time.W, H: 3}))
	ratio, err := pixels.StrokeRatio(img, l.px(s.Time.rect()), fg, bg)
	l.check("type", "lock clock", err == nil && ratio < lfClockStemRatio,
		fmt.Sprintf("stem/cap %.3f, want under %.2f (regular, not bold)%s", ratio, lfClockStemRatio, errText(err)))
}

// windowSeen is a window's frame on the screen, none when there is no such window, and its class.
type windowSeen struct {
	Win *pixels.Rect
	Cls string
}

// lfFocusWindow is the window with the focus.
const lfFocusWindow = `const w = global.display.focus_window; return {win: lfWindow(w), cls: w?.get_wm_class() ?? ''};`

// installer is S3 on the installer's window: its body is f, its outer ring the border on the left
// and the bottom, its headerbar g, and between headerbar and body one solid line of the border.
func (l *lf) installer(img image.Image, s windowSeen) {
	if s.Win == nil || s.Cls != "io.github.construct_linux.Installer" {
		l.check("window", "installer", false, fmt.Sprintf("the focused window is %q, not the installer", s.Cls))
		return
	}
	r := *s.Win
	k := l.k
	// The body: 21 px around the screen's middle, three quarters down, where the window's body is
	// whatever the screen's size (measured at (640, 600) of 1280x800).
	body, _ := pixels.ModeColor(img, l.px(pixels.Rect{X: l.stageW/2 - 10, Y: l.stageH*3/4 - 10, W: 21, H: 21}))
	ring := l.c("dark-border")
	p := l.px(r)
	_, left := pixels.RingHit(img, p.X-1, p.Y+p.H/2, -1, 0, lfRingReach*k, ring)
	_, bottom := pixels.RingHit(img, p.X+p.W/2, p.Y+p.H, 0, 1, lfRingReach*k, ring)
	head, _ := pixels.ModeColor(img, l.px(pixels.Rect{X: r.X + 8, Y: r.Y + 3, W: r.W - 16, H: 8}))
	l.check("window", "installer", pixels.Near(body, l.c("neutral-f"), lfSurfaceTol) && left && bottom && pixels.Near(head, l.c("neutral-g"), lfSurfaceTol),
		fmt.Sprintf("frame %v: body %s (want %s), ring left %v (%s) bottom %v (%s), want %s; headerbar %s (want %s)",
			r, pixels.Hex(body), pixels.Hex(l.c("neutral-f")), left, pixels.Hex(pixels.At(img, p.X-1, p.Y+p.H/2)), bottom, pixels.Hex(pixels.At(img, p.X+p.W/2, p.Y+p.H)),
			pixels.Hex(ring), pixels.Hex(head), pixels.Hex(l.c("neutral-g"))))
	// E3: where g meets f, dark draws one solid line, the border (THEMING, Lines).
	y, c, ok := pixels.HRule(img, p.X+24*k, p.X+p.W-24*k, p.Y+12*k, p.Y+p.H/3, lfSurfaceTol)
	l.check("rule", "installer headerbar", ok && pixels.Near(c, ring, pixels.RingTol),
		fmt.Sprintf("the line under the headerbar at y %d (found %v) is %s, want %s", y, ok, pixels.Hex(c), pixels.Hex(ring)))
}

// lfSettingsWindow is Settings' first window.
const lfSettingsWindow = `return {win: lfWindow(Shell.AppSystem.get_default().lookup_app('org.gnome.Settings.desktop')?.get_windows()[0])};`

// settingsLight is S5, S6 and C11 on Settings in the light scheme, from the pixels: the window on
// the desk with the ring line outside it, the sidebar the shelf, the selected row the white sheet
// with its label in text, and the headerbar's rule one line across both panes.
func (l *lf) settingsLight(img image.Image, s windowSeen) {
	if s.Win == nil {
		l.check("radius", "settings-light window", false, "no Settings window")
		return
	}
	r := *s.Win
	want := int(l.pal.nums["shape-radius-surface"])
	p := l.px(r)
	rad, err := pixels.CornerRadius(img, p.X, p.Y, 2*want*l.k)
	rad /= float64(l.k)
	l.check("radius", "settings-light window", err == nil && math.Abs(rad-float64(want)) <= lfRadiusTol,
		fmt.Sprintf("its top-left corner fits a %.2f px radius, want %d%s", rad, want, errText(err)))
	l.lightFrame("settings-light", img, r)
	l.lightPanes(img, r)
}

// lightFrame is a light window's pixel probes at r, a stage rectangle, named for surface: the desk
// beside it, no bright accent inside it, and the ring line outside it (S5, C2).
func (l *lf) lightFrame(surface string, img image.Image, r pixels.Rect) {
	desk, muted := l.c("light-desk"), l.c("light-muted")
	k := l.k
	// The desk: the widest stretch of desktop beside the window, clear of its shadow and the bars.
	side := pixels.Rect{X: 8, Y: 60, W: r.X - 40, H: l.stageH - 120}
	if right := l.stageW - (r.X + r.W) - 48; right > side.W {
		side = pixels.Rect{X: r.X + r.W + 40, Y: 60, W: right, H: l.stageH - 120}
	}
	side = l.px(side)
	ground, share := pixels.ModeColor(img, side)
	l.check("desk", surface, pixels.Near(ground, desk, lfSurfaceTol),
		fmt.Sprintf("the desktop %s over %.0f%% of %v, want %s", pixels.Hex(ground), share*100, side, pixels.Hex(desk)))
	l.lightAccent(surface+" window", img, r)
	// The ring line, light.muted at its alpha over what is outside the window there - the shadow
	// darkens the desk under it, so the nominal value holds only on a bare desk.
	ringA := l.pal.nums["line-light-ring"]
	p := l.px(r)
	var edges []string
	// The line is a stage pixel, k physical ones: what is outside it is k past its inner pixel.
	for _, e := range []struct {
		name         string
		x, y, nx, ny int
	}{{"top", p.X + p.W/2, p.Y - 1, 0, -1}, {"left", p.X - 1, p.Y + p.H/2, -1, 0}} {
		got, out := pixels.At(img, e.x, e.y), pixels.At(img, e.x+e.nx*k, e.y+e.ny*k)
		if w := pixels.Tint(muted, out, ringA); !pixels.Near(got, w, pixels.RingTol) {
			edges = append(edges, fmt.Sprintf("%s %s, want %s over %s", e.name, pixels.Hex(got), pixels.Hex(w), pixels.Hex(out)))
		}
	}
	l.check("ring", surface+" window", len(edges) == 0,
		fmt.Sprintf("the line outside the window, light.muted at %.0f%% (%s on a bare desk): %v", ringA*100, pixels.Hex(pixels.Tint(muted, desk, ringA)), edges))
}

// lightPanes is Settings' light window with a sidebar at r: the shelf, the selected row the sheet,
// and the headerbar's rule one line across both panes (S5, S6, C11).
func (l *lf) lightPanes(img image.Image, r pixels.Rect) {
	muted := l.c("light-muted")
	k := l.k
	p := l.px(r)
	// The panes: the separator is the first line down the window's left half.
	midY := p.Y + p.H/3
	sep, _, ok := pixels.VRule(img, p.X+60*k, p.X+p.W/2, midY, midY+p.H/6, k, lfSurfaceTol)
	if !ok {
		l.check("panes", "settings-light", false, "no pane separator in the window's left half")
		return
	}
	shelf, _ := pixels.ModeColor(img, pixels.Rect{X: p.X + 4*k, Y: p.Y + 64*k, W: sep - p.X - 8*k, H: p.H - 128*k})
	l.check("shelf", "settings-light", pixels.Near(shelf, l.c("light-shelf"), lfSurfaceTol),
		fmt.Sprintf("the sidebar (%d px) is %s, want %s", (sep-p.X)/k, pixels.Hex(shelf), pixels.Hex(l.c("light-shelf"))))
	l.selectedRow(img, pixels.Rect{X: p.X + 4*k, Y: p.Y + 64*k, W: sep - p.X - 8*k, H: p.H - 72*k})
	// S6: the headerbar's rule at one height in both panes, the border line over each.
	borderA := l.pal.nums["line-light-border"]
	ys, yc, sideOK := pixels.HRule(img, p.X+24*k, sep-24*k, p.Y+8*k, p.Y+80*k, lfSurfaceTol)
	yb, yc2, mainOK := pixels.HRule(img, sep+24*k, p.X+p.W-24*k, p.Y+8*k, p.Y+80*k, lfSurfaceTol)
	above, _ := pixels.ModeColor(img, pixels.Rect{X: sep + 24*k, Y: yb - 3*k, W: p.W - (sep - p.X) - 48*k, H: 1})
	wantRule := pixels.Tint(muted, above, borderA)
	l.check("rule", "settings-light headerbar", sideOK && mainOK && ys == yb && pixels.Near(yc2, wantRule, pixels.RingTol),
		fmt.Sprintf("sidebar's at y %d (%v, %s), content's at y %d (%v, %s); want one y, and light.muted at %.0f%% over %s, %s",
			ys, sideOK, pixels.Hex(yc), yb, mainOK, pixels.Hex(yc2), borderA*100, pixels.Hex(above), pixels.Hex(wantRule)))
}

// stateSeen is a quick toggle shell-states forced into a step, and its title.
type stateSeen struct {
	State string  `json:"state"`
	Look  *lfLook `json:"look"`
	Title *lfLook `json:"title"`
}

const lfStates = lfQuickSettings + `
	return lfAll(m.box, t => t._lfState !== undefined).map(t => ({state: t._lfState, look: look(t), title: look(t._title ?? null)}));`

// states is S2 on the quick toggles shell-states forced into each step - on at rest, under the
// pointer, pressed: each fill is the accent at its step's tint over h, in St and on the screen,
// and each label accent_text.
func (l *lf) states(img image.Image, s []stateSeen) {
	steps := map[string]string{"rest": "tint-dark-rest", "hover": "tint-dark-hover", "active": "tint-dark-active"}
	h, ground, accent := l.c("neutral-h"), l.c("neutral-e"), l.c("dark-accent")
	seen := map[string]bool{}
	var bad []string
	for _, t := range s {
		tok, ok := steps[t.State]
		if !ok || t.Look == nil {
			continue
		}
		seen[t.State] = true
		want := pixels.Tint(accent, h, l.pal.nums[tok])
		st := t.Look.surface(ground)
		drawn, _ := pixels.ModeColor(img, l.px(t.Look.rect()).Inset(4*l.k, 4*l.k))
		if !pixels.Near(st, want, lfTextTol) || !pixels.Near(drawn, want, lfTextTol) {
			bad = append(bad, fmt.Sprintf("%s: St %s, drawn %s, want %s (%s)", t.State, pixels.Hex(st), pixels.Hex(drawn), pixels.Hex(want), tok))
		}
		if t.Title != nil && !pixels.Near(t.Title.ink(ground), l.c("dark-accent-text"), lfTextTol) {
			bad = append(bad, fmt.Sprintf("%s: %q titled %s, not accent_text", t.State, t.Title.Text, pixels.Hex(t.Title.ink(ground))))
		}
	}
	l.check("states", "quick toggles", len(seen) == len(steps) && len(bad) == 0,
		fmt.Sprintf("states forced: %v; off: %v", seen, bad))
}

// lockErrorSeen is the unlock prompt after a wrong password, as the shell drew it.
type lockErrorSeen struct {
	Message *lfLook
	Labels  []*lfLook
	err     error
}

// measureLockError reads the prompt right after the wrong password was typed: GDM shows the
// message for a while and then clears it, so it is read before the view's screenshot, and judged
// once the view is shot (lockErrorCheck, from view), where its checks count towards it.
func (l *lf) measureLockError() {
	if !l.ok {
		return
	}
	var s lockErrorSeen
	s.err = l.g.evalJSON(`const p = Main.screenShield._dialog?._authPrompt ?? null;
		return {message: look(p?._message ?? null), labels: lfLabels(p)};`, &s)
	l.lockError = &s
}

// lockErrorCheck is the prompt after a wrong password: it says so, readable on its surface.
func (l *lf) lockErrorCheck(s lockErrorSeen) {
	if s.err != nil {
		l.check("error", "lock prompt", false, s.err.Error())
		return
	}
	shown := s.Message != nil && s.Message.Mapped && s.Message.Text != ""
	text := ""
	if s.Message != nil {
		text = s.Message.Text
	}
	l.check("error", "lock prompt", shown, fmt.Sprintf("the prompt's message %q after a wrong password", text))
	l.labelsCheck("lock prompt error", s.Labels)
}

// gtkFocus is F2: Tab moved the key focus in a GTK dialog, and what that changed on the screen is
// mostly the accent's outline - dark's accent, or light's in the light scheme - drawn on the
// control it reached and gone from the one it left.
func (l *lf) gtkFocus(name string, img image.Image) {
	ref := strings.Replace(name, "gtk-focus", "gtk-dialog", 1)
	before, ok := l.img(ref)
	if !ok {
		return
	}
	accent := l.c("dark-accent")
	if strings.HasSuffix(name, "-light") {
		accent = l.c("light-accent")
	}
	all := pixels.Rect{X: 0, Y: 0, W: img.Bounds().Dx(), H: img.Bounds().Dy()}
	drawn, share := pixels.FocusDiff(before, img, all, needle.ChannelDelta, accent, lfFocusRingTol)
	least := lfFocusMinChanged * l.k * l.k
	l.check("focus", name, drawn >= least && share >= lfFocusAccentShare,
		fmt.Sprintf("%d px of %s drawn against %s, %.0f%% of the change the ring, drawn or left; want >= %d and >= %.0f%%",
			drawn, pixels.Hex(accent), ref, share*100, least, lfFocusAccentShare*100))
}

// buttonState is S2 on a GTK button at btn, a stage rectangle, in view name against ref, the same
// button a step before: the step reads - another colour - and is a colour C1 allows, the
// palette's or an ink over one.
func (l *lf) buttonState(name, ref string, btn pixels.Rect) {
	if !l.ok {
		return
	}
	img, ok := l.img(name)
	if !ok {
		return
	}
	before, ok := l.img(ref)
	if !ok {
		return
	}
	r := l.px(btn)
	now, _ := pixels.ModeColor(img, r)
	was, _ := pixels.ModeColor(before, r)
	l.check("state", name+" button", !pixels.Near(now, was, lfSurfaceTol) && (l.pal.has(now) || l.pal.composite(now)),
		fmt.Sprintf("the button %s, %s in %s; want another colour, the palette's or an ink over it", pixels.Hex(now), pixels.Hex(was), ref))
}

// gtkLight is a GTK view in the light scheme: Settings' window probes on the window it is about,
// where they reach. A libadwaita dialog dims the window under it and a maximized one has no desk
// or ring, so on those only no bright accent is asked; Text Editor has no sidebar, so no panes.
func (l *lf) gtkLight(name string, img image.Image, r pixels.Rect) {
	switch name {
	case "gtk-popover-light", "gtk-tooltip-light", "gtk-banner-light":
		l.lightFrame(name, img, r)
	default:
		l.lightAccent(name+" window", img, r)
	}
}

// lightAccent is C2 for the accent on light, from the pixels: dark's accent reads 1.5:1 on white,
// so inside a light window, a stage rectangle, every mark, ring and fill is the light accent and
// none of it dark's. The dock's running dot is the shell's, outside the window, and stays cyan.
func (l *lf) lightAccent(surface string, img image.Image, r pixels.Rect) {
	bright, light := l.c("dark-accent"), l.c("light-accent")
	n := pixels.CountNear(img, l.px(r), bright, lfAccentTol)
	l.check("accent", surface, n == 0, fmt.Sprintf("%d px of %s inside %v, want none: light draws %s", n, pixels.Hex(bright), r, pixels.Hex(light)))
}

// selectedRow is C11's navigation sidebar on light: the selected row is the white sheet, its label
// in text. The row is the band of the sidebar whose right part is white.
func (l *lf) selectedRow(img image.Image, side pixels.Rect) {
	sheet := l.c("neutral-a")
	x := side.X + side.W*3/4
	top, bottom := -1, -1
	for y := side.Y; y < side.Y+side.H; y++ {
		if pixels.Near(pixels.At(img, x, y), sheet, lfSurfaceTol) {
			if top < 0 {
				top = y
			}
			bottom = y
		} else if top >= 0 {
			break
		}
	}
	if top < 0 {
		c, _ := pixels.ModeColor(img, side)
		l.check("selected row", "settings-light", false, fmt.Sprintf("no band of %s in the sidebar at x %d (its ground %s)", pixels.Hex(sheet), x, pixels.Hex(c)))
		return
	}
	band := pixels.Rect{X: side.X, Y: top, W: side.W, H: bottom - top + 1}
	darkest, dl := sheet, pixels.Luminance(sheet)
	b := band.Image().Intersect(img.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := pixels.At(img, x, y); pixels.Luminance(c) < dl {
				darkest, dl = c, pixels.Luminance(c)
			}
		}
	}
	text := l.c("light-text")
	l.check("selected row", "settings-light", pixels.Near(darkest, text, lfTextTol),
		fmt.Sprintf("the %s sheet %v; its darkest ink %s, want %s", pixels.Hex(sheet), band, pixels.Hex(darkest), pixels.Hex(text)))
}

// appWindow is the frame of an app's first normal window: a popover open over it is a window of
// its own, the app's newest.
func (l *lf) appWindow(id string) (pixels.Rect, error) {
	var s windowSeen
	if err := l.g.evalJSON(fmt.Sprintf(`return {win: lfWindow(Shell.AppSystem.get_default().lookup_app(%q)?.get_windows().find(w => w.get_window_type() === imports.gi.Meta.WindowType.NORMAL))};`, id), &s); err != nil {
		return pixels.Rect{}, err
	}
	if s.Win == nil {
		return pixels.Rect{}, errors.New("no window")
	}
	return *s.Win, nil
}

// app checks the apps outside GTK that take the desktop's surfaces: Ghostty and VS Code on g and
// f, Chrome off its own dark grey. Firefox's form controls are checked on a page of their own.
func (l *lf) app(id, shot string) {
	if !l.ok {
		return
	}
	switch id {
	case "com.mitchellh.ghostty.desktop", "code.desktop", "com.google.Chrome.desktop", "firefox.desktop":
	default:
		return
	}
	// VS Code greets a fresh user with its onboarding, a modal over a 40% black scrim that dims
	// every surface of the workbench to 0.6 of its colour: Escape, as a person would, and the
	// workbench is what is measured.
	if id == "code.desktop" {
		_ = l.v.Key("esc")
		time.Sleep(2 * time.Second)
		shot = "code-workbench"
		l.r.shotLook(l.v, l.g, viewMeta{shot, needle.GroupApp,
			"VS Code's workbench once its onboarding is dismissed: the title bar and the editor on g, the side and status bars on f, from the palette's construct-theme",
			[]string{"X1", "S3"}})
		l.view(shot)
	}
	l.cur = shot
	img, ok := l.img(shot)
	if !ok {
		return
	}
	if id == "firefox.desktop" {
		l.firefoxControls(id)
		return
	}
	r, err := l.appWindow(id)
	if err != nil {
		l.check("app", shot, false, err.Error())
		return
	}
	l.appSurfaces(id, img, r)
}

// appSurfaces is app's check on an app's window at r, a stage rectangle.
func (l *lf) appSurfaces(id string, img image.Image, r pixels.Rect) {
	g, f := l.c("neutral-g"), l.c("neutral-f")
	region := func(x, y, w, h int) color.NRGBA {
		c, _ := pixels.ModeColor(img, l.px(pixels.Rect{X: r.X + x, Y: r.Y + y, W: w, H: h}))
		return c
	}
	switch id {
	case "com.mitchellh.ghostty.desktop":
		head := region(8, 3, r.W-16, 8)
		term := region(r.W/4, r.H*2/3, r.W/2, r.H/4)
		// Ghostty's own default background, which a missing theme leaves, as a surface: text
		// antialiased on g passes through it a pixel at a time.
		stock := pixels.FlatCount(img, l.px(r), color.NRGBA{0x28, 0x2C, 0x34, 255}, lfSurfaceTol)
		l.check("app", "ghostty", pixels.Near(head, g, lfSurfaceTol) && pixels.Near(term, g, lfSurfaceTol) && stock == 0,
			fmt.Sprintf("headerbar %s, terminal %s, want %s; %d px of a #282C34 surface", pixels.Hex(head), pixels.Hex(term), pixels.Hex(g), stock))
	case "code.desktop":
		title := region(8, 4, r.W-16, 16)
		editor := region(r.W/2, r.H/3, r.W/3, r.H/3)
		side := region(4, r.H/3, 36, r.H/3)
		status := region(8, r.H-18, r.W-16, 12)
		l.check("app", "vscode", pixels.Near(title, g, lfSurfaceTol) && pixels.Near(editor, g, lfSurfaceTol) && pixels.Near(side, f, lfSurfaceTol) && pixels.Near(status, f, lfSurfaceTol),
			fmt.Sprintf("title bar %s and editor %s want %s; side %s and status bar %s want %s",
				pixels.Hex(title), pixels.Hex(editor), pixels.Hex(g), pixels.Hex(side), pixels.Hex(status), pixels.Hex(f)))
	case "com.google.Chrome.desktop":
		// Its title bar in GTK mode is the theme's headerbar, g - on its first window, the terms it
		// asks to be accepted - never Chrome's own #202124, nor a window not yet drawn.
		strip := region(8, 3, r.W-16, 10)
		l.check("app", "chrome", pixels.Near(strip, g, lfSurfaceTol), fmt.Sprintf("title bar %s, want the theme's %s (Chrome's own is #202124)", pixels.Hex(strip), pixels.Hex(g)))
	}
}

// firefoxPage is a light page of the controls GTK draws with the accent: Firefox takes them from
// the theme for the other scheme, which without an Orchis-Construct it finds as Adwaita's blue.
const firefoxPage = `<meta name="color-scheme" content="light"><body style="margin:48px;font:16px sans-serif">` +
	`<p><input type="checkbox" checked> <input type="radio" checked> <input type="range"> ` +
	`<progress value="60" max="100"></progress> <button>Button</button> <input value="text" autofocus></p>`

func (l *lf) firefoxControls(id string) {
	// A file, not a data: URI: Firefox does not navigate to a data: URI it is handed from outside,
	// and showed a blank tab - which the check passed until it asked for the accent.
	page := "/var/home/live/firefox-controls.html"
	if _, err := l.g.run(asLive+`tee `+page+` >/dev/null`, firefoxPage); err != nil {
		l.check("app", "firefox controls", false, "writing the page: "+err.Error())
		return
	}
	uri := "file://" + page
	if err := l.g.evalJSON(fmt.Sprintf(`Shell.AppSystem.get_default().lookup_app(%q).get_app_info().launch_uris([%q], null); return true;`, id, uri), new(bool)); err != nil {
		l.check("app", "firefox controls", false, err.Error())
		return
	}
	time.Sleep(6 * time.Second)
	l.r.shotLook(l.v, l.g, viewMeta{"firefox-controls", needle.GroupApp,
		"A light page of the form controls GTK draws with the accent: Firefox fills them with the light accent through Orchis-Construct, not Adwaita's blue",
		[]string{"X3", "C1"}})
	l.cur = "firefox-controls"
	img, ok := l.img("firefox-controls")
	if !ok {
		return
	}
	// The window the page opened in, as it is now: the one measured before the launch may have
	// moved, or be another.
	win, err := l.appWindow(id)
	if err != nil {
		l.check("app", "firefox controls", false, err.Error())
		return
	}
	l.firefoxAccent(img, win)
}

// firefoxAccent is firefoxControls' check on the page's window at win, a stage rectangle.
func (l *lf) firefoxAccent(img image.Image, win pixels.Rect) {
	blue := color.NRGBA{0x35, 0x84, 0xE4, 255}
	n := pixels.CountNear(img, l.px(win), blue, lfFirefoxColorTol)
	accent := pixels.CountNear(img, l.px(win), l.c("light-accent"), lfFirefoxAccentTol)
	// No Adwaita blue proves nothing alone: a page that never drew has none either.
	l.check("app", "firefox controls", n == 0 && accent >= lfFirefoxAccentMin*l.k*l.k,
		fmt.Sprintf("%d px of Adwaita's #3584E4 on a light page, want none; %d px of %s, want at least %d",
			n, accent, pixels.Hex(l.c("light-accent")), lfFirefoxAccentMin*l.k*l.k))
}

// finish checks what spans views: the boot's mark where the overview's card puts it.
func (l *lf) finish() {
	if !l.ok || l.bootMark == nil || l.cardMark == nil {
		return
	}
	l.cur = ""
	bx, by := l.bootMark[0]*float64(l.stageW*l.k), l.bootMark[1]*float64(l.stageH*l.k)
	dx, dy := math.Abs(bx-l.cardMark[0]), math.Abs(by-l.cardMark[1])
	across, down := float64(lfCardMarkTol*l.k), float64(lfBootMarkDownTol*l.k)
	l.check("mark", "boot to overview", dx <= across && dy <= down,
		fmt.Sprintf("Plymouth's mark at (%.0f, %.0f) on the session's screen, the overview card's at (%.0f, %.0f): want within %.0f px across, %.0f down",
			bx, by, l.cardMark[0], l.cardMark[1], across, down))
}

// maximized is S3's last clause on a window that fills the work area: it keeps neither its corner's
// radius nor the outer ring.
func (l *lf) maximized(id, shot string) {
	if !l.ok {
		return
	}
	l.cur = shot
	img, ok := l.img(shot)
	if !ok {
		return
	}
	r, err := l.appWindow(id)
	if err != nil {
		l.check("window", shot, false, err.Error())
		return
	}
	l.square(img, r)
}

// square is maximized's check on the window at r, a stage rectangle.
func (l *lf) square(img image.Image, r pixels.Rect) {
	head, _ := pixels.ModeColor(img, l.px(pixels.Rect{X: r.X + 8, Y: r.Y + 3, W: r.W - 16, H: 8}))
	p := l.px(r)
	corner := pixels.At(img, p.X, p.Y)
	_, ring := pixels.RingHit(img, p.X-1, p.Y+p.H/2, -1, 0, l.k, l.c("dark-border"))
	l.check("window", "maximized", pixels.Near(corner, head, lfSurfaceTol) && !ring,
		fmt.Sprintf("frame %v: corner %s, headerbar %s (square: the same); the border outside its left edge: %v, want none", r, pixels.Hex(corner), pixels.Hex(head), ring))
}

// guestLook is the checks on the guest itself: the shell's CSS as the running shell loaded it,
// the theme's resources, the fonts and settings, and the files other parts install.
func (l *lf) guestLook() {
	if !l.ok {
		return
	}
	l.cur = ""
	if s, ok := seen[shellSeen](l, "guest", "shell", lfShell); ok {
		l.shell(s)
	}
	l.guestFiles()
}

// shellSeen is what the running shell loaded: its default stylesheet, its resources' toggle-on
// asset and background.js' default colour, and each shown app's icon as the icon theme resolves it.
type shellSeen struct {
	URI, CSS   string
	ToggleOn   bool
	Background string
	Icons      []struct{ ID, Icon, File string }
}

const lfShell = `
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
		return {uri: css ? css.get_uri() : '', css: text, toggleOn, background: bg.split('\n').find(l => l.includes('DEFAULT_BACKGROUND_COLOR =')) ?? bg.slice(0, 160), icons};`

// shell checks what the shell loaded: its CSS against the theme's rules, its resources, and that
// every shown app's icon is Tela's or a brand tile.
func (l *lf) shell(s shellSeen) {
	if s.CSS == "" {
		l.check("css", "shell", false, "the shell has no default stylesheet")
	} else {
		rules := css.Parse(s.CSS)
		ok, detail := cssContract(rules, s.CSS, l.pal)
		l.check("css contract", "G1", ok, detail)
		ok, detail = cssFocus(rules, l.c("dark-accent"))
		l.check("css focus", "G2", ok, detail)
		ok, detail = cssPalette(rules, l.pal)
		l.check("css palette", "G3", ok, detail)
		ok, detail = cssWeights(rules)
		l.check("css weights", "G6", ok, detail)
		ok, detail = cssAccentRings(rules, l.c("dark-accent"))
		l.check("css rings", "C8", ok, detail)
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
}

// guestFiles checks the settings and files other parts install, as the guest reads them.
func (l *lf) guestFiles() {
	user := asLive
	fonts, err := l.g.run(user+`gsettings get org.gnome.desktop.interface font-name; `+user+`gsettings get org.gnome.desktop.interface monospace-font-name; `+
		`for f in monospace sans-serif system-ui; do fc-match -f '%{family[0]}\n' $f; done`, "")
	ui, mono := l.pal.strs["type-ui"], l.pal.strs["type-mono"]
	want := fmt.Sprintf("'%s 11'\n'%s 11'\n%s\n%s\n%s\n", ui, mono, mono, ui, ui)
	l.check("fonts", "G4", err == nil && fonts == want, fmt.Sprintf("font-name, monospace-font-name, fc-match monospace/sans-serif/system-ui: %s; want %s", oneLine(fonts), oneLine(want))+errText(err))

	dconf, err := l.g.run(`for k in dash-to-panel/focus-highlight-color dash-to-panel/focus-highlight-opacity dash-to-panel/trans-use-border tilingshell/window-border-color; do `+asLive+`dconf read /org/gnome/shell/extensions/$k; done`, "")
	accent := pixels.Hex(l.c("dark-accent"))
	// The dock's focus highlight is the rest tint, in dash-to-panel's percent.
	wantDconf := fmt.Sprintf("'%s'\n%.0f\nfalse\n'%s'\n", accent, l.pal.nums["tint-dark-rest"]*100, accent)
	l.check("extensions", "G5 settings", err == nil && strings.EqualFold(dconf, wantDconf), fmt.Sprintf("%s; want %s", oneLine(dconf), oneLine(wantDconf))+errText(err))
	for _, uuid := range []string{"dash-to-panel@jderose9.github.com", "tilingshell@ferrarodomenico.com", "Vitals@CoreCoding.com"} {
		sheet, err := l.g.run("cat /usr/share/gnome-shell/extensions/"+uuid+"/stylesheet.css", "")
		if err != nil {
			l.check("extensions", "G5 "+uuid, false, "no stylesheet.css"+errText(err))
			continue
		}
		ok, detail := cssPalette(css.Parse(sheet), l.pal)
		l.check("extensions", "G5 "+uuid, ok, detail)
	}

	gtk4, err := l.g.run(`grep -oiE -- '--accent-color: *#[0-9a-f]+' /usr/share/themes/Orchis-Construct-Light/gtk-4.0/gtk.css | head -1`, "")
	got := strings.TrimSpace(gtk4[strings.LastIndex(gtk4, "#")+1:])
	l.check("gtk4", "light accent", err == nil && strings.EqualFold("#"+got, pixels.Hex(l.c("light-accent-text"))),
		fmt.Sprintf("Orchis-Construct-Light's gtk-4.0 %s, want --accent-color: %s", oneLine(gtk4), pixels.Hex(l.c("light-accent-text")))+errText(err))
	// T2 in GTK: the CSS libadwaita and GTK 3 apps load from the theme, @media blocks included.
	// libadwaita's own stylesheet under it is not the theme's to change.
	gtkCSS, err := l.g.run(`cat /var/home/live/.config/gtk-4.0/gtk.css /usr/share/themes/Orchis-Construct-Dark/gtk-3.0/gtk.css /usr/share/themes/Orchis-Construct-Light/gtk-3.0/gtk.css`, "")
	if err != nil {
		l.check("css weights", "G6 gtk", false, "the GTK themes' CSS: "+err.Error())
	} else {
		ok, detail := cssWeights(css.Parse(gtkCSS))
		l.check("css weights", "G6 gtk", ok, detail)
	}
	link, err := l.g.run(`readlink /usr/share/themes/Orchis-Construct && test -f /usr/share/themes/Orchis-Construct/gtk-3.0/gtk.css && echo gtk-3.0`, "")
	l.check("firefox theme", "Orchis-Construct", err == nil && link == "Orchis-Construct-Light\ngtk-3.0\n", "link to "+oneLine(link)+errText(err))
	ghostty, err := l.g.run(`test -s "/usr/share/ghostty/themes/Construct Dark" && test -s "/usr/share/ghostty/themes/Construct Light" && echo themes; `+
		`test -s /var/home/live/.config/ghostty/config.ghostty && echo seed; `+
		// From the live user's home: in root's, which live cannot read, ghostty exits 1 and says nothing.
		`out=$(cd /var/home/live && `+user+`ghostty +validate-config 2>&1); rc=$?; echo "$out"; echo "rc=$rc"; ls -l /var/home/live/.config/ghostty; cat /var/home/live/.config/ghostty/config.ghostty`, "")
	// All of it, for a failure that says nothing on the line (rc=1 with no diagnostic, once).
	_ = os.WriteFile(filepath.Join(l.r.dir, "ghostty.txt"), []byte(ghostty), 0o644)
	said, rest, _ := strings.Cut(ghostty, "rc=")
	rc, _, _ := strings.Cut(rest, "\n")
	l.check("ghostty", "themes and seed", err == nil && strings.Contains(said, "themes\nseed\n") && rc == "0",
		oneLine(said)+" rc="+rc+" (ghostty.txt)"+errText(err))
	code, err := l.g.run(`cd /usr/share/code/resources/app/extensions/construct-theme && python3 -c 'import json,glob; fs=["package.json"]+sorted(glob.glob("themes/*.json")); [json.load(open(f)) for f in fs]; print(len(fs))'`, "")
	parsed, _ := strconv.Atoi(strings.TrimSpace(code))
	// package.json and the two themes, dark and light.
	l.check("vscode", "construct-theme", err == nil && parsed >= 3, "JSON files that parse: "+oneLine(code)+errText(err))
	entries, err := l.g.run(`for f in /usr/share/applications/*.desktop /var/lib/flatpak/exports/share/applications/*.desktop /var/home/live/.local/share/applications/*.desktop; do `+
		`[ -f "$f" ] || continue; grep -qE '^(NoDisplay|Hidden)=true' "$f" && continue; grep -q '^Icon=/' "$f" && echo "icon:$f"; case "$f" in *[Ww]olfi*.desktop) echo "wolfi:$f";; esac; done; true`, "")
	l.check("entries", "icons and wolfi", err == nil && strings.TrimSpace(entries) == "", "absolute Icon= or a distrobox entry: "+oneLine(entries)+errText(err))
}
