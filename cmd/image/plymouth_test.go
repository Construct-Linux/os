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

// The theme plays the frames the brand's sequence names, in its order, and refuses a sequence
// that names a frame it has not got or leaves a part empty.
func TestBrandAnimation(t *testing.T) {
	files := map[string][]byte{
		"sequence.json":  []byte(`{"intro":[1,2],"loop":[3,2,3],"outro":[3,1]}`),
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
	if got := scriptArray("loop", seq.Loop); got != "loop[0] = 3; loop[1] = 2; loop[2] = 3; loop_frames = 3;" {
		t.Errorf("scriptArray = %q", got)
	}
	for name, sequence := range map[string]string{
		"missing frame": `{"intro":[1],"loop":[9],"outro":[1]}`,
		"empty part":    `{"intro":[1],"loop":[],"outro":[1]}`,
		"missing part":  `{"intro":[1],"loop":[2]}`,
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
