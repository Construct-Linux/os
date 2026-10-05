package main

import (
	"fmt"
	"image/color"
	"slices"
	"sort"
	"strings"

	"github.com/Construct-Linux/os/internal/css"
	"github.com/Construct-Linux/os/internal/pixels"
)

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
func cssContract(rules []css.Rule, text string, p lfPalette) (bool, string) {
	ring := p.colors["dark-border"]
	var bad []string
	checkOne := func(subject string, bg *color.NRGBA) {
		m, found := css.Subject(rules, subject)
		if !found {
			bad = append(bad, subject+": no rule")
			return
		}
		if bg != nil {
			if c, ok := css.Background(m); !ok || c != (color.NRGBA{bg.R, bg.G, bg.B, 255}) {
				bad = append(bad, fmt.Sprintf("%s: background %q, want %s", subject, m["background-color"]+m["background"], pixels.Hex(*bg)))
			}
		}
		if w, c, ok := css.Border(m); !ok || w != 1 || c != (color.NRGBA{ring.R, ring.G, ring.B, 255}) {
			bad = append(bad, fmt.Sprintf("%s: border %q, want 1px %s", subject, m["border"]+m["border-width"]+m["border-color"], pixels.Hex(ring)))
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
func cssFocus(rules []css.Rule, accent color.NRGBA) (bool, string) {
	none := map[string]bool{}
	var order []string
	rings := 0
	for _, r := range rules {
		var focus []string
		for _, s := range r.Selectors {
			if strings.Contains(s, ":focus") {
				focus = append(focus, s)
			}
		}
		if len(focus) == 0 {
			continue
		}
		for _, d := range r.Decls {
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
			if c := css.Colors(d[1]); strings.HasPrefix(strings.Join(strings.Fields(d[1]), " "), "inset 0 0 0 2px") &&
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
	return len(left) == 0 && rings > 0, fmt.Sprintf("%d rules draw the inset 2px %s ring; box-shadow none on focus: %v", rings, pixels.Hex(accent), left)
}

// cssPalette is G3, C1 as the stylesheet writes it: every colour is the palette's, or white or
// black, at any alpha, or opaque, white, black or the accent at an alpha over a palette colour -
// what St, which stacks no backgrounds, needs written out. A mix() or darken() of two palette
// colours is none of these, and an opaque grey least of all.
func cssPalette(rules []css.Rule, p lfPalette) (bool, string) {
	off := map[string]int{}
	greys := 0
	for _, r := range rules {
		for _, d := range r.Decls {
			for _, c := range css.Colors(d[1]) {
				if c.A == 0 || p.has(c) || (c.R == c.G && c.G == c.B && (c.R == 0 || c.R == 255)) || (c.A == 255 && p.composite(c)) {
					continue
				}
				k := pixels.Hex(c)
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
func cssAccentRings(rules []css.Rule, accent color.NRGBA) (bool, string) {
	isAccent := func(v string) bool {
		for _, c := range css.Colors(v) {
			if c == (color.NRGBA{accent.R, accent.G, accent.B, 255}) {
				return true
			}
		}
		return false
	}
	var bad []string
	for _, r := range rules {
		ring, tinted, width := false, false, -1
		for _, d := range r.Decls {
			switch k, v := d[0], d[1]; {
			case k == "box-shadow":
				for _, sh := range css.SplitOutside(v, ',') {
					if n := css.Lengths(sh); len(n) == 4 && n[0] == 0 && n[1] == 0 && n[2] == 0 && n[3] > 0 && isAccent(sh) {
						ring = true
					}
				}
			case (strings.HasPrefix(k, "border") || strings.HasPrefix(k, "outline")) && !strings.HasSuffix(k, "radius") && k != "outline-offset":
				w, _, _ := css.Border(map[string]string{"border": v})
				width = max(width, w)
				ring = ring || (isAccent(v) && w != 0)
			case k == "background-color" || k == "background":
				for _, c := range css.Colors(v) {
					tinted = tinted || (c.R == accent.R && c.G == accent.G && c.B == accent.B && c.A > 0 && c.A < 255)
				}
			}
		}
		if !ring || (tinted && width == 1) {
			continue
		}
		for _, s := range r.Selectors {
			if !strings.Contains(s, ":focus") && !slices.Contains(cssRingExempt, s) {
				bad = append(bad, s)
			}
		}
	}
	return len(bad) == 0, fmt.Sprintf("accent rings outside :focus: %v", bad)
}

// cssWeights is G6: the shell's text is regular or bold, nothing between or beyond.
func cssWeights(rules []css.Rule) (bool, string) {
	var bad []string
	for _, r := range rules {
		for _, d := range r.Decls {
			if d[0] != "font-weight" {
				continue
			}
			switch d[1] {
			case "normal", "bold", "400", "700", "inherit":
			default:
				bad = append(bad, strings.Join(r.Selectors, ", ")+": "+d[1])
			}
		}
	}
	return len(bad) == 0, fmt.Sprintf("weights other than 400 and 700: %v", bad)
}
