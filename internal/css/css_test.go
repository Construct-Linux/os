package css

import (
	"image/color"
	"slices"
	"testing"
)

// A sheet as St loads it: comments, an @import, a rule in @media, a ; inside url(), and a rule
// that styles a subject in another state.
func TestParseAndSubject(t *testing.T) {
	rules := Parse(`/* the bar */ @import url("a.css");
#panel { background-color: #172028; border: 1px solid #2a3843; }
@media (prefers-color-scheme: dark) {
.osd-window { background-image: url("assets/a;b.svg"); }
}
.popup-menu .popup-menu-content { background-color: #1f2a33; }
.popup-menu .popup-menu-content:hover { background-color: #3584e4; }
#panel.unlock-screen { background-color: transparent; }
@keyframes spin { to { transform: rotate(1turn); } }`)
	if len(rules) != 5 {
		t.Fatalf("%d rules: %v", len(rules), rules)
	}
	if m, _ := Subject(rules, ".osd-window"); m["background-image"] != `url("assets/a;b.svg")` {
		t.Errorf("a ; inside url() split the declaration: %q", m["background-image"])
	}
	if m, _ := Subject(rules, ".popup-menu-content"); m["background-color"] != "#1f2a33" {
		t.Errorf("a :hover rule styled the menu at rest: %v", m)
	}
	m, ok := Subject(rules, "#panel")
	if c, _ := Background(m); !ok || c != (color.NRGBA{0x17, 0x20, 0x28, 255}) {
		t.Errorf("a variant (#panel.unlock-screen) styled the bar: %v", m)
	}
	if w, c, ok := Border(m); !ok || w != 1 || c != (color.NRGBA{0x2A, 0x38, 0x43, 255}) {
		t.Errorf("border %d %v %v", w, c, ok)
	}
	if _, ok := Subject(rules, ".dash-label"); ok {
		t.Error("a subject no rule styles was found")
	}
}

func TestColorsAndLengths(t *testing.T) {
	got := Colors("inset 0 0 0 2px rgba(0, 229, 255, 50%), #fff, transparent")
	want := []color.NRGBA{{0, 229, 255, 128}, {255, 255, 255, 255}, {}}
	if !slices.Equal(got, want) {
		t.Errorf("colours %v", got)
	}
	if n := Lengths("inset 0 0 0 2px rgba(0, 229, 255, 0.5)"); !slices.Equal(n, []float64{0, 0, 0, 2}) {
		t.Errorf("lengths %v", n)
	}
}
