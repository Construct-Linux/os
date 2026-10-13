package qemu

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// QEMU's screendump, a binary P6 PPM with a comment, becomes a PNG of the same pixels.
func TestPPMToPNG(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s.ppm"), filepath.Join(dir, "s.png")
	ppm := "P6\n# QEMU\n2 1\n255\n" + string([]byte{255, 0, 0, 0, 0, 255})
	if err := os.WriteFile(src, []byte(ppm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ppmToPNG(src, dst); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("bounds %v", b)
	}
	if r, _, b, _ := img.At(0, 0).RGBA(); r>>8 != 255 || b != 0 {
		t.Errorf("pixel 0 is not red")
	}
	if r, _, b, _ := img.At(1, 0).RGBA(); r != 0 || b>>8 != 255 {
		t.Errorf("pixel 1 is not blue")
	}
}
