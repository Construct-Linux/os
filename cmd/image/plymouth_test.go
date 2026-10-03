package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// The prompt goes under the logo's last opaque row, not under the frame's transparent margin.
func TestOpaqueBottom(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 20))
	img.Set(3, 2, color.NRGBA{0, 0, 0, 255})
	img.Set(7, 14, color.NRGBA{0, 0xe5, 0xff, 1})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	got, err := opaqueBottom(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.75 {
		t.Errorf("opaqueBottom = %v, want 0.75", got)
	}
	var empty bytes.Buffer
	if err := png.Encode(&empty, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if _, err := opaqueBottom(empty.Bytes()); err == nil {
		t.Error("an empty frame has no bottom, and must say so")
	}
}
