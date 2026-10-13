package qemu

import (
	"os"
	"path/filepath"
	"testing"
)

// A screendump is a file QEMU wrote, read back by the harness: whatever its bytes, DecodePPM
// answers an error, or an image of at least one pixel whose pixels the file held, opaque - never a
// panic, nor memory for pixels the file does not have.
func FuzzDecodePPM(f *testing.F) {
	pixels := string([]byte{255, 0, 0, 0, 0, 255})
	for _, s := range []string{
		"P6\n# QEMU\n2 1\n255\n" + pixels,
		"P6 2 1 255 " + pixels,
		"P6\n2 1\n255\n" + pixels[:5],
		"P6\n-2 -1\n255\n" + pixels,
		"P6\n0 1\n255\n",
		"P6\n2147483647 2147483647\n255\n" + pixels,
		"P6\n4611686018427387904 2\n255\n" + pixels,
		"P6\n2 1\n65535\n" + pixels + pixels,
		"P3\n2 1\n255\n255 0 0 0 0 255\n",
		"P6\n#",
		"",
	} {
		f.Add([]byte(s))
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "s.ppm")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		img, err := DecodePPM(p)
		if err != nil {
			return
		}
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		if w < 1 || h < 1 || w*h*3 > len(data) {
			t.Fatalf("a %dx%d image from %d bytes", w, h, len(data))
		}
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] != 255 {
				t.Fatalf("pixel %d is not opaque", i/4)
			}
		}
	})
}
