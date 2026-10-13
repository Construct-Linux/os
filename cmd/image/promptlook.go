package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"sort"

	"github.com/Construct-Linux/os/internal/pixels"
)

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
	prompt, err := pixels.ReadPNG(filepath.Join(dir, "password.png"))
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
		after, err := pixels.ReadPNG(f)
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
