package qemu

import (
	"os"
	"path/filepath"
	"testing"
)

// QEMU's screendump, a binary P6 PPM with a comment, decodes to its pixels.
func TestDecodePPM(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.ppm")
	ppm := "P6\n# QEMU\n2 1\n255\n" + string([]byte{255, 0, 0, 0, 0, 255})
	if err := os.WriteFile(src, []byte(ppm), 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := DecodePPM(src)
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

// A header that claims no screen, or one larger than the file's pixels or any screen, is refused
// rather than taken for a panic or gigabytes of memory.
func TestAPPMHeaderIsNotTrusted(t *testing.T) {
	pixels := string([]byte{255, 0, 0, 0, 0, 255})
	for _, ppm := range []string{
		"P6\n-2 -1\n255\n" + pixels,
		"P6\n0 1\n255\n" + pixels,
		"P6\n2 2\n255\n" + pixels,
		"P6\n100000 100000\n255\n" + pixels,
		"P6\n4611686018427387904 2\n255\n" + pixels,
	} {
		p := filepath.Join(t.TempDir(), "s.ppm")
		if err := os.WriteFile(p, []byte(ppm), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePPM(p); err == nil {
			t.Errorf("%q decoded", ppm)
		}
	}
}
