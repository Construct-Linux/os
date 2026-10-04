package main

import (
	"fmt"
	"image/color"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// cssRule is one rule of a stylesheet: its selectors, and its declarations in order, each
// property lowercased and its value without !important.
type cssRule struct {
	selectors []string
	decls     [][2]string
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// parseCSS reads St's and GTK's CSS: rules of selectors and declarations. The rules inside @media
// and @supports are read as rules of the sheet, whichever scheme or support they ask for; other
// @-rules are skipped.
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
		if strings.HasPrefix(sel, "@media") || strings.HasPrefix(sel, "@supports") {
			rules = append(rules, parseCSS(body)...)
			continue
		}
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
// focus (F1). Calendar days are not: today is a label, so their focus is the ring.
var cssFocusExempt = []string{"popup-menu-item"}

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

// cssPalette is G3, C1 as the stylesheet writes it: every colour is the palette's, or white or
// black, at any alpha, or opaque, white, black or the accent at an alpha over a palette colour -
// what St, which stacks no backgrounds, needs written out. A mix() or darken() of two palette
// colours is none of these, and an opaque grey least of all.
func cssPalette(rules []cssRule, p lfPalette) (bool, string) {
	off := map[string]int{}
	greys := 0
	for _, r := range rules {
		for _, d := range r.decls {
			for _, c := range cssColors(d[1]) {
				if c.A == 0 || p.has(c) || (c.R == c.G && c.G == c.B && (c.R == 0 || c.R == 255)) || (c.A == 255 && p.composite(c)) {
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

// cssRingExempt are the accent rings that are focus, though no :focus selects them (THEMING, the
// accent scale's level 5): the Alt+Esc frame around the window it will focus.
var cssRingExempt = []string{".cycler-highlight"}

// cssAccentRings is C8: an accent ring - an outline, a border, or a box-shadow with no offset and
// no blur - only on :focus. A 1 px border on the accent at an alpha is decoration's edge (the
// rubberband, the tile preview: level 7), not a ring.
func cssAccentRings(rules []cssRule, accent color.NRGBA) (bool, string) {
	isAccent := func(v string) bool {
		for _, c := range cssColors(v) {
			if c == (color.NRGBA{accent.R, accent.G, accent.B, 255}) {
				return true
			}
		}
		return false
	}
	var bad []string
	for _, r := range rules {
		ring, tinted, width := false, false, -1
		for _, d := range r.decls {
			switch k, v := d[0], d[1]; {
			case k == "box-shadow":
				for _, sh := range splitOutside(v, ',') {
					f := strings.Fields(cssRGB.ReplaceAllString(sh, ""))
					var n []float64
					for _, t := range f {
						if x, err := strconv.ParseFloat(strings.TrimSuffix(t, "px"), 64); err == nil {
							n = append(n, x)
						}
					}
					if len(n) == 4 && n[0] == 0 && n[1] == 0 && n[2] == 0 && n[3] > 0 && isAccent(sh) {
						ring = true
					}
				}
			case (strings.HasPrefix(k, "border") || strings.HasPrefix(k, "outline")) && !strings.HasSuffix(k, "radius") && k != "outline-offset":
				w, _, _ := cssRing(map[string]string{"border": v})
				width = max(width, w)
				ring = ring || (isAccent(v) && w != 0)
			case k == "background-color" || k == "background":
				for _, c := range cssColors(v) {
					tinted = tinted || (c.R == accent.R && c.G == accent.G && c.B == accent.B && c.A > 0 && c.A < 255)
				}
			}
		}
		if !ring || (tinted && width == 1) {
			continue
		}
		for _, s := range r.selectors {
			if !strings.Contains(s, ":focus") && !slices.Contains(cssRingExempt, s) {
				bad = append(bad, s)
			}
		}
	}
	return len(bad) == 0, fmt.Sprintf("accent rings outside :focus: %v", bad)
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
