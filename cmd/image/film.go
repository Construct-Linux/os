package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Construct-Linux/os/internal/pixels"
	"github.com/Construct-Linux/os/internal/qemu"
)

// The boot film: the screen from the firmware to the shell's first frame, every filmInterval.
const (
	filmInterval = 100 * time.Millisecond
	// A frame is Plymouth's when the mark's cyan is on it and the rest is the boot's black.
	filmMarkPixels = 400
	filmBlackShare = 0.85
	// No wrong ground may cover more than this share of a frame once Plymouth has started.
	filmMaxCover = 0.40
	// systemd-boot's selected entry is a bar of the firmware console's grey at least this long.
	filmMenuRun = 120
)

// filmGrounds are what must not cover the screen once Plymouth has drawn: GNOME's
// SystemBackground (#282828, which recipes/gnome-shell patches to black), GTK's dark window
// (#222226), and f (#11181E), a window's body, which the overview and lock grounds are not.
var filmGrounds = []color.NRGBA{{0x28, 0x28, 0x28, 255}, {0x22, 0x22, 0x26, 255}, {0x11, 0x18, 0x1E, 255}}

// filmFrame is what one frame of the boot showed.
type filmFrame struct {
	at time.Duration
	// The frame's size: the firmware's framebuffer until the virtio GPU's driver takes the
	// screen at the panel's.
	w, h     int
	cyan     int
	mark     pixels.Rect
	black    float64
	menu     bool
	cover    float64
	coverHex string
}

// markHue is the mark's lines, cyan or teal: bright (the wordmark's faint cyan glow is not), its red
// well under its green and blue (the wordmark is white), and its green near its blue (the
// firmware's logo is blue).
func markHue(r, g, b uint8) bool {
	return min(g, b) > markLineLevel && int(r)*2 < int(min(g, b)) && int(g)*4 >= int(b)*3
}

// markLineLevel is how bright the mark's lines are in green and blue at the least: the overview's
// teal (#0097A7) is above it, the glow around the wordmark under it.
const markLineLevel = 0x80

func (f filmFrame) plymouth() bool { return f.cyan >= filmMarkPixels && f.black >= filmBlackShare }

// filmStats reads a frame in one pass: the mark's cyan and its extents, the black, systemd-boot's
// menu bar, and the largest share a wrong ground covers.
func filmStats(img *image.RGBA) filmFrame {
	b := img.Bounds()
	var f filmFrame
	box := image.Rectangle{}
	black := 0
	cover := make([]int, len(filmGrounds))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		run, runC := 0, [3]uint8{}
		for x := b.Min.X; x < b.Max.X; x++ {
			i := img.PixOffset(x, y)
			r, g, bl := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
			if max(r, g, bl) <= 2 {
				black++
			}
			if markHue(r, g, bl) {
				f.cyan++
				p := image.Rect(x, y, x+1, y+1)
				if box.Empty() {
					box = p
				} else {
					box = box.Union(p)
				}
			}
			for k, c := range filmGrounds {
				if pixels.ChannelDelta(color.NRGBA{r, g, bl, 255}, c) <= lfSurfaceTol {
					cover[k]++
				}
			}
			grey := r >= 0x60 && r <= 0xE0 && abs(int(r)-int(g)) <= 2 && abs(int(g)-int(bl)) <= 2
			if grey && run > 0 && runC == [3]uint8{r, g, bl} {
				run++
			} else if grey {
				run, runC = 1, [3]uint8{r, g, bl}
			} else {
				run = 0
			}
			if run >= filmMenuRun {
				f.menu = true
			}
		}
	}
	n := float64(b.Dx() * b.Dy())
	f.w, f.h = b.Dx(), b.Dy()
	f.black = float64(black) / n
	f.mark = pixels.Rect{X: box.Min.X, Y: box.Min.Y, W: box.Dx(), H: box.Dy()}
	for k, c := range cover {
		if s := float64(c) / n; s > f.cover {
			f.cover, f.coverHex = s, pixels.Hex(filmGrounds[k])
		}
	}
	return f
}

// film records the boot while the harness waits for it.
type film struct {
	stopc  chan struct{}
	done   chan struct{}
	frames []filmFrame
	failed int
}

// startFilm screenshots v every filmInterval into dir and reads each frame a tick after asking for
// it, so QEMU has written it; the frames worth looking at are kept as PNGs when it stops.
func startFilm(v *qemu.VM, dir string) *film {
	f := &film{stopc: make(chan struct{}), done: make(chan struct{})}
	_ = os.MkdirAll(dir, 0o755)
	go func() {
		defer close(f.done)
		start := time.Now()
		keep := map[string]*image.RGBA{}
		var worst float64
		pending, pendingAt := "", time.Duration(0)
		read := func() {
			if pending == "" {
				return
			}
			img, err := qemu.DecodePPM(pending)
			_ = os.Remove(pending)
			pending = ""
			if err != nil {
				f.failed++
				return
			}
			s := filmStats(img)
			s.at = pendingAt
			f.frames = append(f.frames, s)
			started := false
			for _, x := range f.frames {
				started = started || x.plymouth()
			}
			switch {
			case s.menu && keep["menu"] == nil:
				keep["menu"] = img
			case s.plymouth() && keep["plymouth"] == nil:
				keep["plymouth"] = img
			}
			if started && s.cover > worst {
				worst, keep["worst-ground"] = s.cover, img
			}
			keep["last"] = img
		}
		t := time.NewTicker(filmInterval)
		defer t.Stop()
		for n := 0; ; n++ {
			select {
			case <-f.stopc:
				read()
				for name, img := range keep {
					_ = writePNG(filepath.Join(dir, name+".png"), img)
				}
				return
			case <-t.C:
			}
			read()
			p := filepath.Join(dir, fmt.Sprintf("%05d.ppm", n))
			if v.Screendump(p) == nil {
				pending, pendingAt = p, time.Since(start)
			}
		}
	}()
	return f
}

func (f *film) stop() []filmFrame {
	close(f.stopc)
	<-f.done
	return f.frames
}

// filmReport checks the boot's film: once Plymouth has drawn, no wrong ground covers the screen;
// how long systemd-boot's menu showed; and where Plymouth put the mark, as shares of its screen,
// for lf.finish to hold against the overview's card.
func (r *report) filmReport(frames []filmFrame, failed int) *[2]float64 {
	first := -1
	for i, f := range frames {
		if f.plymouth() {
			first = i
			break
		}
	}
	if first < 0 {
		r.add("boot film", false, fmt.Sprintf("%d frames (%d unreadable), none of Plymouth's mark on black: see film/", len(frames), failed))
		return nil
	}
	var worst filmFrame
	for _, f := range frames[first:] {
		if f.cover > worst.cover {
			worst = f
		}
	}
	// How long systemd-boot's menu showed is a measurement, not a check: it goes with the ground's.
	var menuFrom, menuTo time.Duration = -1, -1
	for _, f := range frames[:first] {
		if f.menu {
			if menuFrom < 0 {
				menuFrom = f.at
			}
			menuTo = f.at
		}
	}
	menu := "never on screen"
	if menuFrom >= 0 {
		menu = fmt.Sprintf("on screen %.1fs, from %.1fs", (menuTo - menuFrom + filmInterval).Seconds(), menuFrom.Seconds())
	}
	r.add("boot film: ground", worst.cover <= filmMaxCover,
		fmt.Sprintf("%d frames from Plymouth at %.1fs; the most a wrong ground covered: %s %.0f%% at %.1fs, want <= %.0f%% (film/worst-ground.png); systemd-boot's menu %s",
			len(frames)-first, frames[first].at.Seconds(), worst.coverHex, worst.cover*100, worst.at.Seconds(), filmMaxCover*100, menu))
	// The mark once it is whole - the intro draws it piece by piece - on the last screen Plymouth
	// drew on: it starts on the firmware's framebuffer, and its frames there are another size.
	last := frames[first]
	for _, f := range frames[first:] {
		if f.plymouth() {
			last = f
		}
	}
	panel := func(f filmFrame) bool { return f.plymouth() && f.w == last.w && f.h == last.h }
	largest := 0
	for _, f := range frames[first:] {
		if panel(f) {
			largest = max(largest, f.mark.W*f.mark.H)
		}
	}
	var xs, ys []float64
	for _, f := range frames[first:] {
		if panel(f) && f.mark.W*f.mark.H*10 >= largest*9 {
			xs = append(xs, float64(f.mark.X)+float64(f.mark.W)/2)
			ys = append(ys, float64(f.mark.Y)+float64(f.mark.H)/2)
		}
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	return &[2]float64{xs[len(xs)/2] / float64(last.w), ys[len(ys)/2] / float64(last.h)}
}

// The rows Plymouth's prompt is looked for in, as shares of the screen's height: below the mark and
// its wordmark, around the pill's middle (rows 560 to 680 of 800, the pill's middle at 612). The
// script lays the mark and the logo out in shares of the height, and the prompt a fixed distance
// under the logo, so the shares hold within the band at 1440x900 too.
const promptFromShare, promptToShare = 560.0 / 800, 680.0 / 800

// Plymouth's prompt is a 320x36 pill with a 2 px ring (plymouth.go, entry.png): its inside is
// sampled over the middle half of it, 6 px under the ring's top and 24 high, clear of the ring and
// the round ends; its left end on the ring's top row is outside the pill, the boot's black. The
// ring down the middle column is flat, within promptRingTol of the accent.
const (
	promptPillHalfW   = 160
	promptInsideHalfW = 80
	promptInsideTop   = 6
	promptInsideH     = 24
	promptRingTol     = 8
	// gridBack counts the mark's cyan this far above the band at the least, clear of the title
	// over the field, which starts at the band's top.
	promptGridMargin = 20
)

// promptRows is the band of rows the prompt is looked for in, in a screenshot h px high.
func promptRows(h int) (int, int) {
	return int(math.Round(promptFromShare * float64(h))), int(math.Round(promptToShare * float64(h)))
}

// promptLook checks Plymouth's password prompt as test:lifecycle shows it: a pill on g with an
// accent ring, centred under the mark on the boot's black. The ring is looked for down the middle
// column; the pill's sizes are Plymouth's pixels, plymouthScale physical ones each.
func promptLook(img image.Image, accent, fill, ground color.NRGBA) (bool, string) {
	cx := img.Bounds().Dx() / 2
	k := plymouthScale(img.Bounds().Dx(), img.Bounds().Dy())
	promptFrom, promptTo := promptRows(img.Bounds().Dy())
	top := -1
	for y := promptFrom; y < min(promptTo, img.Bounds().Dy()); y++ {
		if pixels.Near(pixels.At(img, cx, y), accent, promptRingTol) {
			top = y
			break
		}
	}
	if top < 0 {
		return false, fmt.Sprintf("no %s ring at x %d from y %d to %d", pixels.Hex(accent), cx, promptFrom, promptTo)
	}
	inside, _ := pixels.ModeColor(img, pixels.Rect{X: cx - promptInsideHalfW*k, Y: top + promptInsideTop*k, W: 2 * promptInsideHalfW * k, H: promptInsideH * k})
	corner := pixels.At(img, cx-promptPillHalfW*k, top)
	ok := pixels.Near(inside, fill, lfTextTol) && pixels.Near(corner, ground, lfTextTol)
	return ok, fmt.Sprintf("ring at y %d; inside %s (want %s), the pill's corner %s (want %s)", top, pixels.Hex(inside), pixels.Hex(fill), pixels.Hex(corner), pixels.Hex(ground))
}

// plymouthPrompt checks test:lifecycle's password prompt (password.png) and the screen 1.5 s after
// the password (password-after.png), in the palette of the system the disk then booted.
func (r *report) plymouthPrompt(g *guest, dir string) {
	raw, err := g.run("cat /"+brandPalette, "")
	if err != nil {
		r.add("lf: prompt plymouth", false, "/"+brandPalette+" is not on the guest"+errText(err))
		return
	}
	p, err := parseLFPalette([]byte(raw))
	if err != nil {
		r.add("lf: prompt plymouth", false, err.Error())
		return
	}
	prompt, err := readPNG(filepath.Join(dir, "password.png"))
	if err != nil {
		r.add("lf: prompt plymouth", false, err.Error())
		return
	}
	ok, detail := promptLook(prompt, p.colors["dark-accent"], p.colors["neutral-g"], p.colors["brand-boot"])
	r.add("lf: prompt plymouth", ok, detail+" (password.png)")
	afters, _ := filepath.Glob(filepath.Join(dir, "password-after-*.png"))
	sort.Strings(afters)
	grid, why := gridPlymouth(prompt, afters)
	r.add("lf: grid plymouth", grid, why)
}

// gridPlymouth is whether any of the screens after the password shows the grid back, the outro
// going back. Its result is its own: screens it cannot read are no grid, whatever the prompt was.
func gridPlymouth(prompt image.Image, afters []string) (bool, string) {
	why := "no readable screen after the password"
	from, _ := promptRows(prompt.Bounds().Dy())
	for _, f := range afters {
		after, err := readPNG(f)
		if err != nil {
			why = err.Error()
			continue
		}
		ok, detail := gridBack(prompt, after, from)
		if ok {
			return true, detail
		}
		why = detail + " (" + filepath.Base(f) + ")"
	}
	return false, why
}

// gridBack is whether the grid came back after the password: above the prompt there is at least
// this much more of the mark's cyan than the outro left with the prompt.
func gridBack(prompt, after image.Image, promptTop int) (bool, string) {
	const more = 1.25
	count := func(img image.Image) int {
		n := 0
		for y := 0; y < promptTop-promptGridMargin; y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				c := pixels.At(img, x, y)
				if markHue(c.R, c.G, c.B) {
					n++
				}
			}
		}
		return n
	}
	a, b := count(prompt), count(after)
	return float64(b) >= more*float64(a) && a > 0, fmt.Sprintf("%d px of the mark with the prompt, %d after, want %.2fx", a, b, more)
}
