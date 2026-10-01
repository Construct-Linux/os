package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// themeDir is where plymouthTheme writes the theme, laid into the root as a layer.
var themeDir = filepath.Join(out, "plymouth")

// throbberFrames is how many frames the ring under the logo turns through: at Plymouth's 30 fps,
// one turn a second.
const throbberFrames = 30

// plymouthTheme writes the "spin" theme for Plymouth's two-step plugin: SpinBot in the middle as
// the watermark, and under it a ring of the logo's cyans turning - a comet's arc that fades along
// its tail - while the machine boots, until GDM takes the screen.
func plymouthTheme() error {
	dir := filepath.Join(themeDir, "usr/share/plymouth/themes/spin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	conf := `[Plymouth Theme]
Name=Spin
Description=SpinBot, and a ring that turns while the machine boots
ModuleName=two-step

[two-step]
ImageDir=/usr/share/plymouth/themes/spin
WatermarkHorizontalAlignment=.5
WatermarkVerticalAlignment=.42
HorizontalAlignment=.5
VerticalAlignment=.66
Transition=none
TransitionDuration=0.0
BackgroundStartColor=0x000000
BackgroundEndColor=0x000000
ProgressBarBackgroundColor=0x000000
ProgressBarForegroundColor=0x000000
MessageBelowAnimation=true
`
	if err := os.WriteFile(filepath.Join(dir, "spin.plymouth"), []byte(conf), 0o644); err != nil {
		return err
	}
	logoBytes, err := os.ReadFile(logo)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "watermark.png"), logoBytes, 0o644); err != nil {
		return err
	}
	// two-step shows throbber-NNNN.png while it waits, and animation-NNNN.png when it ends; the
	// same ring for both, so the end is not a jump.
	for i := range throbberFrames {
		img := ringFrame(float64(i) / throbberFrames)
		for _, name := range []string{"throbber", "animation"} {
			if err := writePNG(filepath.Join(dir, fmt.Sprintf("%s-%04d.png", name, i+1)), img); err != nil {
				return err
			}
		}
	}
	// Two-step's other images: a lock and the entry box for a password prompt.
	for name, img := range map[string]image.Image{
		"lock.png":   ringFrame(0),
		"entry.png":  box(320, 36, color.RGBA{0x10, 0x1c, 0x2c, 0xff}),
		"bullet.png": dot(10, color.RGBA{0x4f, 0xc3, 0xf7, 0xff}),
	} {
		if err := writePNG(filepath.Join(dir, name), img); err != nil {
			return err
		}
	}
	conf = "[Daemon]\nTheme=spin\nShowDelay=0\nDeviceTimeout=8\n"
	etc := filepath.Join(themeDir, "etc/plymouth")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(etc, "plymouthd.conf"), []byte(conf), 0o644)
}

// ringFrame is the ring at phase (0 to 1 of a turn): an arc of three quarters whose head is the
// logo's bright cyan and whose tail fades to nothing, antialiased.
func ringFrame(phase float64) image.Image {
	const size, radius, width = 64, 26.0, 5.0
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	head := color.NRGBA{0x4f, 0xc3, 0xf7, 0xff}
	c := float64(size) / 2
	for y := range size {
		for x := range size {
			dx, dy := float64(x)+.5-c, float64(y)+.5-c
			d := math.Hypot(dx, dy)
			edge := width/2 - math.Abs(d-radius) // >0 inside the stroke
			if edge <= -1 {
				continue
			}
			cover := math.Min(1, edge+1)
			// The angle behind the head, as a fraction of a turn: 0 at the head.
			a := math.Atan2(dy, dx)/(2*math.Pi) + .5
			behind := math.Mod(phase-a+2, 1)
			if behind > .75 {
				continue
			}
			alpha := cover * (1 - behind/.75)
			img.SetNRGBA(x, y, color.NRGBA{head.R, head.G, head.B, uint8(alpha * 255)})
		}
	}
	return img
}

func box(w, h int, c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

func dot(d int, c color.RGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, d, d))
	r := float64(d) / 2
	for y := range d {
		for x := range d {
			if math.Hypot(float64(x)+.5-r, float64(y)+.5-r) <= r {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

func writePNG(p string, img image.Image) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
