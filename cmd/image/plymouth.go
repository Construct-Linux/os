package main

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
)

// themeDir is where plymouthTheme writes the theme, laid into the root as a layer and copied
// into the initrd.
var themeDir = filepath.Join(out, "plymouth")

// brandFrames is where construct-brand (recipes/, from github.com/Construct-Linux/brand) puts the
// boot animation: its frames, frame-NNNN.png, and sequence.json, which frames each part plays -
// the intro once, the loop while the machine boots, the outro until the logo is left. A frame may
// appear in more than one part, and more than once in one.
const brandFrames = "usr/share/construct/plymouth"

// brandSequence is sequence.json: each part's frames, by number, in the order they play.
type brandSequence struct {
	Intro []int `json:"intro"`
	Loop  []int `json:"loop"`
	Outro []int `json:"outro"`
}

func frameName(n int) string { return fmt.Sprintf("frame-%04d.png", n) }

// themeScript is the theme's script, for Plymouth's script plugin: the screen is CONSTRUCT's, the
// disk's password prompt included, which two-step would leave to systemd's words ("Please enter
// LUKS2 token PIN").
//
//go:embed plymouth/construct.script
var themeScript string

// plymouthTheme writes the "construct" theme: the brand's animation, its intro once and its loop
// while the machine boots, until GDM takes the screen; when the disk asks for its password, the
// outro - the grid goes out, the logo stays - and the prompt under it. Plymouth leaves as soon as
// the boot is done, so the outro is the password's, not the boot's end.
//
// The frames come from the root's construct-brand and go into the theme at the same path, so the
// initrd, which takes the theme and not the root's packages, has them too. "construct" speaks
// English; "construct-<locale>" each language whose words GNOME has and Adwaita Sans draws
// (bootprompt.go), sharing its images: an installed machine's initrd is told its person's
// (construct install, a credential the boot-language unit writes into plymouthd.conf).
func plymouthTheme() error {
	rootfs := filepath.Join(out, "rootfs.tar")
	files, err := readBrandFiles(rootfs)
	if err != nil {
		return err
	}
	seq, frames, err := brandAnimation(files)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(themeDir); err != nil {
		return err
	}
	for name, b := range frames {
		dst := filepath.Join(themeDir, brandFrames, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	logoBottom, err := opaqueBottom(frames[frameName(seq.Outro[len(seq.Outro)-1])])
	if err != nil {
		return err
	}
	themes := filepath.Join(themeDir, "usr/share/plymouth/themes")
	// Plymouth's script reads an image by a path it puts under the theme's ImageDir, so the frames
	// are reached from there.
	framesFromTheme, err := filepath.Rel("/usr/share/plymouth/themes/construct", "/"+brandFrames)
	if err != nil {
		return err
	}
	dir := filepath.Join(themes, "construct")
	if err := os.MkdirAll(filepath.Join(dir, "lang"), 0o755); err != nil {
		return err
	}
	langs, err := promptLanguages(rootfs)
	if err != nil {
		return err
	}
	langs[""] = englishPrompt
	tmpl, err := template.New("construct.script").Parse(themeScript)
	if err != nil {
		return err
	}
	for lang, words := range langs {
		name, script := "construct", "construct.script"
		if lang != "" {
			name, script = "construct-"+lang, filepath.Join("lang", lang+".script")
		}
		var b bytes.Buffer
		if err := tmpl.Execute(&b, struct {
			promptWords
			Intro, Loop, Outro         string
			Frames, LogoBottom         string
			KeyPINPrompt, TouchMessage string
		}{scriptQuoted(words), scriptArray("intro", seq.Intro), scriptArray("loop", seq.Loop), scriptArray("outro", seq.Outro),
			framesFromTheme + "/", fmt.Sprintf("%.3f", logoBottom), keyPINPrompt, touchMessage}); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, script), b.Bytes(), 0o644); err != nil {
			return err
		}
		conf := fmt.Sprintf("[Plymouth Theme]\nName=Construct (%s)\nDescription=CONSTRUCT's boot animation, and the disk's password prompt\nModuleName=script\n\n[script]\nImageDir=/usr/share/plymouth/themes/construct\nScriptFile=/usr/share/plymouth/themes/construct/%s\n", words.Lang, script)
		if err := os.MkdirAll(filepath.Join(themes, name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(themes, name, name+".plymouth"), []byte(conf), 0o644); err != nil {
			return err
		}
	}
	// The brand's palette (github.com/Construct-Linux/brand palette.toml): the field is neutral g,
	// what Orchis draws text on in dark mode; the dots are the mark's cyan.
	for name, img := range map[string]image.Image{
		"entry.png":  box(320, 36, color.RGBA{0x17, 0x20, 0x28, 0xff}),
		"bullet.png": dot(10, color.RGBA{0x00, 0xe5, 0xff, 0xff}),
	} {
		if err := writePNG(filepath.Join(dir, name), img); err != nil {
			return err
		}
	}
	conf := "[Daemon]\nTheme=construct\nShowDelay=0\nDeviceTimeout=8\n"
	etc := filepath.Join(themeDir, "etc/plymouth")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(etc, "plymouthd.conf"), []byte(conf), 0o644)
}

// readBrandFiles reads construct-brand's animation from the root's tarball, by file name.
func readBrandFiles(rootfs string) (map[string][]byte, error) {
	f, err := os.Open(rootfs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	frames := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if path.Dir(name) != brandFrames {
			continue
		}
		if frames[path.Base(name)], err = io.ReadAll(tr); err != nil {
			return nil, err
		}
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("%s has no %s: image/apko.yaml installs construct-brand", rootfs, brandFrames)
	}
	return frames, nil
}

// brandAnimation is the brand's sequence and the frames it plays, by file name: every part has
// frames, and every frame it names is there. A frame no part plays stays out of the theme.
func brandAnimation(files map[string][]byte) (brandSequence, map[string][]byte, error) {
	var seq brandSequence
	b, ok := files["sequence.json"]
	if !ok {
		return seq, nil, fmt.Errorf("construct-brand has no %s/sequence.json", brandFrames)
	}
	if err := json.Unmarshal(b, &seq); err != nil {
		return seq, nil, fmt.Errorf("construct-brand's sequence.json: %w", err)
	}
	frames := map[string][]byte{}
	for _, part := range []struct {
		name   string
		frames []int
	}{{"intro", seq.Intro}, {"loop", seq.Loop}, {"outro", seq.Outro}} {
		if len(part.frames) == 0 {
			return seq, nil, fmt.Errorf("construct-brand's sequence.json has no %s", part.name)
		}
		for _, n := range part.frames {
			name := frameName(n)
			f, ok := files[name]
			if !ok {
				return seq, nil, fmt.Errorf("construct-brand's %s plays %s, which it has not got", part.name, name)
			}
			frames[name] = f
		}
	}
	return seq, frames, nil
}

// scriptArray is a Plymouth script's statements filling the array name with v, the language
// having no array literal: name[0] = 1; name[1] = 2; ...
func scriptArray(name string, v []int) string {
	var b strings.Builder
	for i, n := range v {
		fmt.Fprintf(&b, "%s[%d] = %d; ", name, i, n)
	}
	fmt.Fprintf(&b, "%s_frames = %d;", name, len(v))
	return b.String()
}

// opaqueBottom is how far down a frame its drawing reaches, as a share of its height: the prompt
// goes under the logo, not under the frame's transparent margin.
func opaqueBottom(b []byte) (float64, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	r := img.Bounds()
	for y := r.Max.Y - 1; y >= r.Min.Y; y-- {
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				return float64(y-r.Min.Y+1) / float64(r.Dy()), nil
			}
		}
	}
	return 0, errors.New("the outro's last frame is empty")
}

// scriptQuoted escapes the words for a Plymouth script's double-quoted strings.
func scriptQuoted(w promptWords) promptWords {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	return promptWords{Lang: q(w.Lang), Password: q(w.Password), Caps: q(w.Caps), Retry: q(w.Retry), KeyPIN: q(w.KeyPIN), Touch: q(w.Touch)}
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
