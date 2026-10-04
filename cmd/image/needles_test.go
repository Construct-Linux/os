package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// A view differs by the share of its compared pixels a theme change moved, not by noise, and
// leaves out the top bar and what its .json ignores.
func TestCompareNeedle(t *testing.T) {
	dir := t.TempDir()
	ink := color.NRGBA{0x0B, 0x11, 0x16, 255}
	ref := filepath.Join(dir, "ref.png")
	if err := writePNG(ref, solid(100, 100, ink)); err != nil {
		t.Fatal(err)
	}
	shot := func(name string, img image.Image) string {
		p := filepath.Join(dir, name+".png")
		if err := writePNG(p, img); err != nil {
			t.Fatal(err)
		}
		return p
	}
	diff := filepath.Join(dir, "diff.png")

	// Noise under the channel tolerance: nothing differs.
	noisy := solid(100, 100, color.NRGBA{0x0B + 10, 0x11, 0x16, 255})
	if share, err := compareNeedle(ref, shot("noisy", noisy), "", diff); err != nil || share != 0 {
		t.Errorf("noise: %v, %v", share, err)
	}

	// A 10x10 square of another colour, below the top bar: 100 of the compared pixels.
	changed := solid(100, 100, ink)
	for y := 50; y < 60; y++ {
		for x := 10; x < 20; x++ {
			changed.SetNRGBA(x, y, color.NRGBA{0x1F, 0x2A, 0x33, 255})
		}
	}
	share, err := compareNeedle(ref, shot("changed", changed), "", diff)
	if err != nil {
		t.Fatal(err)
	}
	// The top bar's region (x 640.., not in a 100px image) leaves all 10000 compared.
	if want := 100.0 / 10000; share != want {
		t.Errorf("share = %v, want %v", share, want)
	}

	// The same square ignored by the view's .json: nothing differs.
	spec := filepath.Join(dir, "ref.json")
	if err := os.WriteFile(spec, []byte(`{"ignore":[{"x":10,"y":50,"w":10,"h":10,"why":"a clock face"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if share, err := compareNeedle(ref, shot("changed", changed), spec, diff); err != nil || share != 0 {
		t.Errorf("ignored: %v, %v", share, err)
	}

	// Another size is a failure of its own.
	if _, err := compareNeedle(ref, shot("small", solid(50, 50, ink)), "", diff); err == nil {
		t.Error("a different size: no error")
	}
}
