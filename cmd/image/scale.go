package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// screen is the harness's display: its panel in physical pixels and the session's scale on it.
// The machine Construct is drawn for is the ThinkPad X9-15, a 2880x1800 panel at 15.3" (about
// 222 dpi), which GNOME runs at 200%, 1440x900 logical, as a MacBook Pro runs its Retina panel:
// at 1x every view is soft whatever the theme does, and a review on a 2x screen sees it.
type screen struct{ W, H, Scale int }

var screenX9 = screen{2880, 1800, 2}

// logical is the session's stage: the panel over its scale.
func (s screen) logical() (int, int) { return s.W / s.Scale, s.H / s.Scale }

// scaleOf is how many physical pixels a stage pixel is, from a screenshot's width and the
// stage's: a whole number at the scales the harness measures at, false at a fractional one.
func scaleOf(physical, stage int) (int, bool) {
	if stage <= 0 || physical <= 0 || physical%stage != 0 {
		return 0, false
	}
	return physical / stage, true
}

// absPoint is a stage point on the USB tablet's absolute scale, 0-32767 across the stage.
func absPoint(x, y, stageW, stageH int) (int, int) {
	return x * 32767 / max(1, stageW-1), y * 32767 / max(1, stageH-1)
}

// gdctlScale reads the first logical monitor's scale from gdctl show.
func gdctlScale(show string) (float64, bool) {
	for _, line := range strings.Split(show, "\n") {
		_, v, ok := strings.Cut(line, "Scale:")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

// liveActive fails, saying which sessions the seat has, unless the live user's session on seat0
// is the active one: a greeter in front of it is a boot that went wrong, not one to work around.
const liveActive = `s=$(loginctl list-sessions --no-legend | awk '$3 == "live" && $4 == "seat0" {print $1; exit}') && ` +
	`{ [ "$(loginctl show-session "$s" -p Active --value)" = yes ] || { echo "the live session is not the seat's active one:"; loginctl list-sessions --no-legend; false; }; } && `

// gdctlMonitor is the shell command that puts the first monitor's connector in $c.
const gdctlMonitor = `c=$(` + asLive + `gdctl show | grep -o 'Monitor [^ ]*' | head -1 | cut -d' ' -f2) && `

// setSessionScale sets the session's monitor to scale through mutter's own D-Bus API, as Settings'
// Displays panel does, and waits until the shell's stage is the logical size it makes of the
// panel. It is temporary - nothing is written to monitors.xml - so the run's image stays the
// image a person boots.
//
// gdctl and not a monitors.xml or the scaling-factor key: a monitors.xml applies only to a
// monitor whose connector, vendor, product and serial match QEMU's, and is written into a home
// the live session makes at boot; scaling-factor is a global factor mutter takes for every
// monitor of every machine. gdctl asks mutter for the scale on the monitor there is, and answers
// whether mutter took it.
//
// The live session must be the seat's active one before and after, or every view after shoots
// another session's screen.
func (g *guest) setSessionScale(scale float64, wantW, wantH int) (string, error) {
	out, err := g.run(liveActive+gdctlMonitor+asLive+fmt.Sprintf(`gdctl set --logical-monitor --primary --monitor "$c" --scale %g 2>&1 && `, scale)+asLive+`gdctl show 2>&1 && `+liveActive+`echo active`, "")
	if err != nil {
		return out, fmt.Errorf("gdctl: %w: %s", err, oneLine(out))
	}
	got, ok := gdctlScale(out)
	if !ok || math.Abs(got-scale) > 0.01 {
		return out, fmt.Errorf("gdctl show says scale %v, asked for %g", got, scale)
	}
	if wantW == 0 {
		return out, nil
	}
	var w, h int
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		var st [2]int
		if g.evalJSON(`return [global.stage.width, global.stage.height];`, &st) == nil {
			w, h = st[0], st[1]
			if w == wantW && h == wantH {
				return out, nil
			}
		}
	}
	return out, fmt.Errorf("the stage is %dx%d at scale %g, want %dx%d", w, h, scale, wantW, wantH)
}

// sessionScreen sets the session to s's scale and says so in the report: every view after it is
// drawn at s.Scale, and a run whose session is not cannot claim its screenshots are.
func (r *report) sessionScreen(g *guest, s screen) {
	w, h := s.logical()
	out, err := g.setSessionScale(float64(s.Scale), w, h)
	r.add("screen", err == nil, fmt.Sprintf("%dx%d at %d00%%, stage %dx%d: %s%s", s.W, s.H, s.Scale, w, h, oneLine(lastLines(out, 12)), errText(err)))
	// Mutter lays the panel and the shell's chrome out again, and apps redraw at the new scale.
	time.Sleep(3 * time.Second)
}

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// plymouthScale is the scale Plymouth draws at on a screen of w x h when it guessed it: it starts
// on simpledrm, whose firmware framebuffer carries no physical size, guesses (2 from 2880x1620,
// 1800x1200 on a 3:2 panel) and keeps guessing once the native driver takes over
// (src/libply/ply-utils.c, get_device_scale_guess). Without a size, the VM's virtio GPU and the
// X9's panel both get 2; with the X9's EDID (15.3", 222 dpi over mutter's 135 target, 1.64 >
// 1.625) the physical rule gives 2 as well.
func plymouthScale(w, h int) int {
	if h > w {
		w, h = h, w
	}
	if w*2 == h*3 {
		if w >= 1800 && h >= 1200 {
			return 2
		}
		return 1
	}
	if w >= 2880 && h >= 1620 {
		return 2
	}
	return 1
}
