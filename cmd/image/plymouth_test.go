package main

import (
	"archive/tar"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The prompt goes under the logo's last opaque row, not under the frame's transparent margin.
func TestOpaqueBottom(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 20))
	img.Set(3, 2, color.NRGBA{0, 0, 0, 255})
	img.Set(7, 14, color.NRGBA{0, 0xe5, 0xff, 1})
	got, err := opaqueBottom(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.75 {
		t.Errorf("opaqueBottom = %v, want 0.75", got)
	}
	if _, err := opaqueBottom(encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 4, 4)))); err == nil {
		t.Error("an empty frame has no bottom, and must say so")
	}
}

// The theme plays the frames the brand's sequence names, in its order, and refuses a sequence
// that names a frame it has not got or leaves a part empty.
func TestBrandAnimation(t *testing.T) {
	files := map[string][]byte{
		"sequence.json":  []byte(`{"intro":[1,2],"loop":[3,2,3],"outro":[3,1],"mark_center":0.4329}`),
		"frame-0001.png": []byte("1"), "frame-0002.png": []byte("2"), "frame-0003.png": []byte("3"),
		"frame-0004.png": []byte("unplayed"),
	}
	seq, frames, err := brandAnimation(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || frames["frame-0004.png"] != nil {
		t.Errorf("frames = %v, want the three played", frames)
	}
	if got := scriptArray("loop", seq.Loop); got != `loop[0] = "frame-0003.png"; loop[1] = "frame-0002.png"; loop[2] = "frame-0003.png"; loop_frames = 3;` {
		t.Errorf("scriptArray = %q", got)
	}
	for name, sequence := range map[string]string{
		"missing frame":       `{"intro":[1],"loop":[9],"outro":[1],"mark_center":0.4}`,
		"empty part":          `{"intro":[1],"loop":[],"outro":[1],"mark_center":0.4}`,
		"missing part":        `{"intro":[1],"loop":[2],"mark_center":0.4}`,
		"no mark_center":      `{"intro":[1],"loop":[2],"outro":[1]}`,
		"mark_center outside": `{"intro":[1],"loop":[2],"outro":[1],"mark_center":1.2}`,
	} {
		files["sequence.json"] = []byte(sequence)
		if _, _, err := brandAnimation(files); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	delete(files, "sequence.json")
	if _, _, err := brandAnimation(files); err == nil {
		t.Error("no sequence.json: no error")
	}
}

// The mark is the rows brighter than the wallpaper's grid; its centre is their middle, so the
// boot's mark lands where the wallpaper draws it. An image with nothing above the grid has no mark.
func TestMarkCenter(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 100))
	for x := range 8 {
		img.Set(x, 2, color.RGBA{0x28, 0x28, 0x28, 0xff}) // the grid, at the threshold: not the mark
		img.Set(x, 90, color.RGBA{0x10, 0x20, 0x28, 0xff})
	}
	img.Set(4, 30, color.RGBA{0, 0x29, 0, 0xff}) // one channel above the grid is enough
	img.Set(1, 49, color.RGBA{0xe6, 0xed, 0xf2, 0xff})
	got, err := markCenter(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.4 {
		t.Errorf("markCenter = %v, want 0.4 (rows 30 to 49 of 100)", got)
	}
	img.Set(4, 30, color.RGBA{0, 0, 0, 0xff})
	img.Set(1, 49, color.RGBA{0, 0, 0, 0xff})
	if _, err := markCenter(encodePNG(t, img)); err == nil {
		t.Error("a wallpaper with only its grid has no mark, and must say so")
	}
}

// The palette's colors are read by name, flat and #RRGGBB; one it has not got, or not a color,
// is an error, never a black prompt.
func TestPaletteColors(t *testing.T) {
	palette := []byte(`{"neutral-g":"#172028","dark-accent":"#00E5FF","shape-inset":6,"tint":"0.16"}`)
	got, err := paletteColors(palette, "neutral-g", "dark-accent")
	if err != nil {
		t.Fatal(err)
	}
	if got["neutral-g"] != (color.RGBA{0x17, 0x20, 0x28, 0xff}) || got["dark-accent"] != (color.RGBA{0x00, 0xe5, 0xff, 0xff}) {
		t.Errorf("paletteColors = %v", got)
	}
	if s := scriptColor(got["dark-accent"]); s != "0.000, 0.898, 1.000" {
		t.Errorf("scriptColor = %q", s)
	}
	for _, key := range []string{"dark-text", "shape-inset", "tint"} {
		if _, err := paletteColors(palette, key); err == nil {
			t.Errorf("%s: no error", key)
		}
	}
}

// The prompt's field is the shell's unlock entry: an opaque g pill with a 2 px accent ring, its
// corners left to the black behind it and its curve anti-aliased; the dots are round, in text.
func TestPromptImages(t *testing.T) {
	g, accent, text := color.RGBA{0x17, 0x20, 0x28, 0xff}, color.RGBA{0x00, 0xe5, 0xff, 0xff}, color.RGBA{0xe6, 0xed, 0xf2, 0xff}
	nrgba := func(c color.RGBA) color.NRGBA { return color.NRGBA{c.R, c.G, c.B, c.A} }
	entry := pill(320, 36, 2, g, accent).(*image.NRGBA)
	if b := entry.Bounds(); b.Dx() != 320 || b.Dy() != 36 {
		t.Fatalf("entry is %v", b)
	}
	for _, p := range []struct {
		x, y int
		want color.NRGBA
	}{
		{160, 0, nrgba(accent)}, {160, 1, nrgba(accent)}, {160, 2, nrgba(g)}, {160, 18, nrgba(g)},
		{160, 34, nrgba(accent)}, {160, 35, nrgba(accent)},
		{1, 18, nrgba(accent)}, {318, 18, nrgba(accent)}, {18, 18, nrgba(g)},
		{0, 0, color.NRGBA{}}, {319, 35, color.NRGBA{}},
	} {
		if got := entry.NRGBAAt(p.x, p.y); got != p.want {
			t.Errorf("entry at (%d,%d) = %v, want %v", p.x, p.y, got, p.want)
		}
	}
	// Along the curve, some pixel is neither inside nor outside: the edge is not a staircase.
	partial := false
	for y := range 18 {
		if a := entry.NRGBAAt(y/3, y).A; a > 0 && a < 0xff {
			partial = true
		}
	}
	if !partial {
		t.Error("the entry's curve has no partly covered pixel")
	}

	bullet := pill(10, 10, 0, text, text).(*image.NRGBA)
	if got := bullet.NRGBAAt(5, 5); got != nrgba(text) {
		t.Errorf("bullet's centre = %v, want %v", got, nrgba(text))
	}
	if got := bullet.NRGBAAt(0, 0); got.A != 0 {
		t.Errorf("bullet's corner = %v, want transparent", got)
	}
}

// A root without the brand's palette or wallpaper fails the theme's build by name, rather than
// drawing a prompt in no colors or a mark at no height.
func TestReadBrandFiles(t *testing.T) {
	write := func(files ...string) string {
		p := filepath.Join(t.TempDir(), "rootfs.tar")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(f)
		for _, name := range files {
			if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	frame := brandFrames + "/frame-0001.png"
	brand, err := readBrandFiles(write(frame, brandPalette, brandWallpaper, "usr/share/construct/other"))
	if err != nil {
		t.Fatal(err)
	}
	if len(brand.frames) != 1 || brand.frames["frame-0001.png"] == nil || brand.palette == nil || brand.wallpaper == nil {
		t.Errorf("readBrandFiles = %+v", brand)
	}
	for missing, files := range map[string][]string{
		brandFrames:    {brandPalette, brandWallpaper},
		brandPalette:   {frame, brandWallpaper},
		brandWallpaper: {frame, brandPalette},
	} {
		if _, err := readBrandFiles(write(files...)); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: %v", missing, err)
		}
	}
}
