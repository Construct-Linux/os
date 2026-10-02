package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// themeDir is where plymouthTheme writes the theme, laid into the root as a layer and copied
// into the initrd.
var themeDir = filepath.Join(out, "plymouth")

// throbberFrames is how many frames the ring under the logo turns through: at Plymouth's 30 fps,
// one turn a second.
const throbberFrames = 30

// spinScript is the theme's script, for Plymouth's script plugin: the screen is Spindrift's,
// the disk's password prompt included, which two-step would leave to systemd's words ("Please
// enter LUKS2 token PIN").
//
//go:embed plymouth/spin.script
var spinScript string

// plymouthTheme writes the "spin" theme: SpinBot in the middle, under it a ring of the logo's
// cyans turning - a comet's arc that fades along its tail - while the machine boots, until GDM
// takes the screen, and the password prompt the disk asks for in its place. "spin" speaks English;
// "spin-<locale>" each language whose words GNOME has and Adwaita Sans draws (bootprompt.go),
// sharing spin's images: an installed machine's initrd is told its person's (drift install, a
// credential the boot-language unit writes into plymouthd.conf).
func plymouthTheme() error {
	themes := filepath.Join(themeDir, "usr/share/plymouth/themes")
	dir := filepath.Join(themes, "spin")
	if err := os.MkdirAll(filepath.Join(dir, "lang"), 0o755); err != nil {
		return err
	}
	langs, err := promptLanguages(filepath.Join(out, "rootfs.tar"))
	if err != nil {
		return err
	}
	langs[""] = englishPrompt
	tmpl, err := template.New("spin.script").Parse(spinScript)
	if err != nil {
		return err
	}
	for lang, words := range langs {
		name, script := "spin", "spin.script"
		if lang != "" {
			name, script = "spin-"+lang, filepath.Join("lang", lang+".script")
		}
		var b bytes.Buffer
		if err := tmpl.Execute(&b, struct {
			promptWords
			Frames int
		}{scriptQuoted(words), throbberFrames}); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, script), b.Bytes(), 0o644); err != nil {
			return err
		}
		conf := fmt.Sprintf("[Plymouth Theme]\nName=Spin (%s)\nDescription=SpinBot, a ring that turns while the machine boots, and the disk's password prompt\nModuleName=script\n\n[script]\nImageDir=/usr/share/plymouth/themes/spin\nScriptFile=/usr/share/plymouth/themes/spin/%s\n", words.Lang, script)
		if err := os.MkdirAll(filepath.Join(themes, name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(themes, name, name+".plymouth"), []byte(conf), 0o644); err != nil {
			return err
		}
	}
	logoBytes, err := os.ReadFile(logo)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "watermark.png"), logoBytes, 0o644); err != nil {
		return err
	}
	for i := range throbberFrames {
		if err := writePNG(filepath.Join(dir, fmt.Sprintf("ring-%d.png", i)), ringFrame(float64(i)/throbberFrames)); err != nil {
			return err
		}
	}
	for name, img := range map[string]image.Image{
		"entry.png":  box(320, 36, color.RGBA{0x10, 0x1c, 0x2c, 0xff}),
		"bullet.png": dot(10, color.RGBA{0x4f, 0xc3, 0xf7, 0xff}),
	} {
		if err := writePNG(filepath.Join(dir, name), img); err != nil {
			return err
		}
	}
	conf := "[Daemon]\nTheme=spin\nShowDelay=0\nDeviceTimeout=8\n"
	etc := filepath.Join(themeDir, "etc/plymouth")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(etc, "plymouthd.conf"), []byte(conf), 0o644)
}

// scriptQuoted escapes the words for a Plymouth script's double-quoted strings.
func scriptQuoted(w promptWords) promptWords {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	return promptWords{Lang: q(w.Lang), Password: q(w.Password), Caps: q(w.Caps), Retry: q(w.Retry)}
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
