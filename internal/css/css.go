// Package css reads St's and GTK's stylesheets: their rules, the declarations that style a
// subject at rest, and the colours and borders in a value.
package css

import (
	"image/color"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Construct-Linux/os/internal/pixels"
)

// Rule is one rule of a stylesheet: its selectors, and its declarations in order, each
// property lowercased and its value without !important.
type Rule struct {
	Selectors []string
	Decls     [][2]string
}

var comment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// Parse reads St's and GTK's CSS: rules of selectors and declarations. The rules inside @media
// and @supports are read as rules of the sheet, whichever scheme or support they ask for; other
// @-rules are skipped.
func Parse(text string) []Rule {
	text = comment.ReplaceAllString(text, "")
	var rules []Rule
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
			rules = append(rules, Parse(body)...)
			continue
		}
		if strings.HasPrefix(sel, "@") {
			continue
		}
		var r Rule
		for _, s := range strings.Split(sel, ",") {
			r.Selectors = append(r.Selectors, strings.Join(strings.Fields(s), " "))
		}
		for _, d := range SplitOutside(body, ';') {
			k, v, ok := strings.Cut(d, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
			r.Decls = append(r.Decls, [2]string{strings.ToLower(strings.TrimSpace(k)), v})
		}
		rules = append(rules, r)
	}
}

// SplitOutside splits s at sep where it is not inside parentheses or quotes: url("a;b") stays one.
func SplitOutside(s string, sep byte) []string {
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

var simple = regexp.MustCompile(`[.#:]{0,2}[-\w]+`)

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
	want := simple.FindAllString(target, -1)
	have := simple.FindAllString(last, -1)
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

// Subject merges the declarations of every rule whose subject carries target, later over earlier.
func Subject(rules []Rule, target string) (map[string]string, bool) {
	m := map[string]string{}
	found := false
	for _, r := range rules {
		for _, s := range r.Selectors {
			if subjectHas(s, target) {
				found = true
				for _, d := range r.Decls {
					m[d[0]] = d[1]
				}
				break
			}
		}
	}
	return m, found
}

var rgb = regexp.MustCompile(`rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+%?)\s*)?\)`)

// Colors are the colour literals in a CSS value: hex, rgb() and rgba(), and the keywords
// transparent, black and white.
func Colors(v string) []color.NRGBA {
	var out []color.NRGBA
	for _, m := range rgb.FindAllStringSubmatch(v, -1) {
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
	for _, w := range strings.FieldsFunc(rgb.ReplaceAllString(v, " "), func(r rune) bool { return r == ' ' || r == ',' || r == '(' || r == ')' }) {
		switch {
		case strings.HasPrefix(w, "#"):
			if c, ok := pixels.ParseHex(w); ok {
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

// Background is a declaration set's background colour: background-color's, else the background
// shorthand's.
func Background(m map[string]string) (color.NRGBA, bool) {
	for _, k := range []string{"background-color", "background"} {
		if v, ok := m[k]; ok {
			if c := Colors(v); len(c) > 0 {
				return c[0], true
			}
		}
	}
	return color.NRGBA{}, false
}

// Border is a declaration set's border: its width in px and its colour.
func Border(m map[string]string) (int, color.NRGBA, bool) {
	width, col, okC := -1, color.NRGBA{}, false
	parse := func(v string) {
		for _, f := range strings.Fields(rgb.ReplaceAllString(v, "")) {
			if n, err := strconv.Atoi(strings.TrimSuffix(f, "px")); err == nil && strings.HasSuffix(f, "px") {
				width = n
			}
			if f == "none" || f == "0" {
				width = 0
			}
		}
		if c := Colors(v); len(c) > 0 {
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

// Lengths are the numbers in a value outside its rgb() and rgba() colours, px or unitless: a
// box-shadow's offsets, blur and spread.
func Lengths(v string) []float64 {
	var n []float64
	for _, t := range strings.Fields(rgb.ReplaceAllString(v, "")) {
		if x, err := strconv.ParseFloat(strings.TrimSuffix(t, "px"), 64); err == nil {
			n = append(n, x)
		}
	}
	return n
}
