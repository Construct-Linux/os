package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"

	"golang.org/x/image/bmp"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// logo is SpinBot, spin-ami's bare metal's splash: 256 pixels square with its transparency.
const logo = "image/logo/spinbot.png"

// splashLine is one line of a splash's text and how many times the font's size it is.
type splashLine struct {
	text  string
	scale int
}

// splash is the image systemd-stub shows while the kernel image starts (ukify --splash), until
// Plymouth takes the screen: the logo and lines of white text under it, centred on black, in the
// 7x13 fixed font scaled up - spin-ami's internal/disk splash, for the desktop.
func splash(lines ...splashLine) ([]byte, error) {
	f, err := os.Open(logo)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	art, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	face := basicfont.Face7x13
	const pad = 16
	w, h := art.Bounds().Dx(), pad+art.Bounds().Dy()+pad
	for _, l := range lines {
		w = max(w, font.MeasureString(face, l.text).Ceil()*l.scale)
		h += face.Height*l.scale + pad
	}
	w += 2 * pad
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	at := image.Pt((w-art.Bounds().Dx())/2, pad)
	draw.Draw(img, art.Bounds().Sub(art.Bounds().Min).Add(at), art, art.Bounds().Min, draw.Over)
	y := pad + art.Bounds().Dy() + pad
	for _, l := range lines {
		tw := font.MeasureString(face, l.text).Ceil()
		small := image.NewRGBA(image.Rectangle{Max: image.Pt(tw, face.Height)})
		d := font.Drawer{Dst: small, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(0, face.Ascent)}
		d.DrawString(l.text)
		x := (w - tw*l.scale) / 2
		for sy := range face.Height {
			for sx := range tw {
				c := small.RGBAAt(sx, sy)
				if c.A == 0 {
					continue
				}
				draw.Draw(img, image.Rect(x+sx*l.scale, y+sy*l.scale, x+(sx+1)*l.scale, y+(sy+1)*l.scale),
					image.NewUniform(c), image.Point{}, draw.Src)
			}
		}
		y += face.Height*l.scale + pad
	}
	var b bytes.Buffer
	if err := bmp.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
