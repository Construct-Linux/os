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

// brandPalette is construct-brand's palette, flat: the prompt is drawn in the desktop's colors,
// read from the same file the desktop's themes are made from, so it never keeps an old copy.
const brandPalette = "usr/share/construct/palette.json"

// brandWallpaper is the dark wallpaper, which draws the mark where GDM and the desktop show it:
// the boot puts its own mark at the same height, so the logo does not jump at the handover.
const brandWallpaper = "usr/share/backgrounds/construct/construct-dark.png"

// brandSequence is sequence.json: each part's frames, by number, in the order they play, and
// where the mark's centre is in a frame, as a share of its height.
type brandSequence struct {
	Intro      []int   `json:"intro"`
	Loop       []int   `json:"loop"`
	Outro      []int   `json:"outro"`
	MarkCenter float64 `json:"mark_center"`
}

// brandFiles is what the theme takes from construct-brand in the root.
type brandFiles struct {
	frames             map[string][]byte // brandFrames' files, by name
	palette, wallpaper []byte
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
// outro - the grid goes out, the logo stays - and the prompt under it, and once the disk is open
// the outro backwards into the loop, so GDM takes over on the grid the wallpaper draws. Plymouth
// leaves as soon as the boot is done, so the outro is the password's, not the boot's end. The
// mark sits where the dark wallpaper draws it, and the prompt is drawn in the palette's colors.
//
// The frames come from the root's construct-brand and go into the theme at the same path, so the
// initrd, which takes the theme and not the root's packages, has them too. "construct" speaks
// English; "construct-<locale>" each language whose words GNOME has and Adwaita Sans draws
// (bootprompt.go), sharing its images: an installed machine's initrd is told its person's
// (construct install, a credential the boot-language unit writes into plymouthd.conf).
func plymouthTheme() error {
	rootfs := filepath.Join(out, "rootfs.tar")
	brand, err := readBrandFiles(rootfs)
	if err != nil {
		return err
	}
	seq, frames, err := brandAnimation(brand.frames)
	if err != nil {
		return err
	}
	colors, err := paletteColors(brand.palette, "brand-boot", "neutral-g", "dark-accent", "dark-text", "dark-warning", "dark-error")
	if err != nil {
		return err
	}
	markAt, err := markCenter(brand.wallpaper)
	if err != nil {
		return fmt.Errorf("%s: %w", brandWallpaper, err)
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
			Intro, Loop, Outro                 string
			Frames, LogoBottom                 string
			MarkCenter, FrameMarkCenter        string
			Boot, Text, Warning, Error, Accent string
			KeyPINPrompt, TouchMessage         string
		}{scriptQuoted(words), scriptArray("intro", seq.Intro), scriptArray("loop", seq.Loop), scriptArray("outro", seq.Outro),
			framesFromTheme + "/", fmt.Sprintf("%.3f", logoBottom),
			fmt.Sprintf("%.4f", markAt), fmt.Sprintf("%.4f", seq.MarkCenter),
			scriptColor(colors["brand-boot"]), scriptColor(colors["dark-text"]), scriptColor(colors["dark-warning"]),
			scriptColor(colors["dark-error"]), scriptColor(colors["dark-accent"]),
			keyPINPrompt, touchMessage}); err != nil {
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
	// The field is the shell's unlock entry: a pill on g, the bars' surface, ringed in the accent
	// as a focused entry is, with the dots in the text's color.
	for name, img := range map[string]image.Image{
		"entry.png":  pill(320, 36, 2, colors["neutral-g"], colors["dark-accent"]),
		"bullet.png": pill(10, 10, 0, colors["dark-text"], colors["dark-text"]),
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

// readBrandFiles reads construct-brand's animation, palette and dark wallpaper from the root's
// tarball.
func readBrandFiles(rootfs string) (brandFiles, error) {
	brand := brandFiles{frames: map[string][]byte{}}
	f, err := os.Open(rootfs)
	if err != nil {
		return brand, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return brand, err
		}
		name := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if name != brandPalette && name != brandWallpaper && path.Dir(name) != brandFrames {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return brand, err
		}
		switch name {
		case brandPalette:
			brand.palette = b
		case brandWallpaper:
			brand.wallpaper = b
		default:
			brand.frames[path.Base(name)] = b
		}
	}
	for what, missing := range map[string]bool{
		brandFrames:    len(brand.frames) == 0,
		brandPalette:   brand.palette == nil,
		brandWallpaper: brand.wallpaper == nil,
	} {
		if missing {
			return brand, fmt.Errorf("%s has no %s: image/apko.yaml installs construct-brand, and recipes/construct-brand.yaml puts it there", rootfs, what)
		}
	}
	return brand, nil
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
	if seq.MarkCenter <= 0 || seq.MarkCenter >= 1 {
		return seq, nil, fmt.Errorf("construct-brand's sequence.json has no mark_center inside the frame (%v)", seq.MarkCenter)
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

// markLevel is the brightness, out of 255, above which the wallpaper's pixels are the mark: its
// grid and glow stay under it.
const markLevel = 40

// markCenter is where an image's mark is centred, as a share of its height: the middle of the
// rows that have a pixel brighter than markLevel in any channel.
func markCenter(b []byte) (float64, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	r := img.Bounds()
	bright := func(y int) bool {
		for x := r.Min.X; x < r.Max.X; x++ {
			if cr, cg, cb, _ := img.At(x, y).RGBA(); max(cr, cg, cb)>>8 > markLevel {
				return true
			}
		}
		return false
	}
	top := r.Min.Y
	for top < r.Max.Y && !bright(top) {
		top++
	}
	if top == r.Max.Y {
		return 0, errors.New("no mark: nothing in it is brighter than the grid")
	}
	bottom := r.Max.Y - 1
	for !bright(bottom) {
		bottom--
	}
	return float64(top+bottom+1-2*r.Min.Y) / 2 / float64(r.Dy()), nil
}

// paletteColors reads keys' colors from construct-brand's palette.json, flat, each "#RRGGBB": a
// key it has not got is an error, never black.
func paletteColors(b []byte, keys ...string) (map[string]color.RGBA, error) {
	var palette map[string]json.RawMessage
	if err := json.Unmarshal(b, &palette); err != nil {
		return nil, fmt.Errorf("%s: %w", brandPalette, err)
	}
	colors := map[string]color.RGBA{}
	for _, k := range keys {
		var s string
		if raw, ok := palette[k]; !ok {
			return nil, fmt.Errorf("%s has no %s", brandPalette, k)
		} else if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%s's %s: %w", brandPalette, k, err)
		}
		c := color.RGBA{A: 0xff}
		if _, err := fmt.Sscanf(s, "#%02x%02x%02x", &c.R, &c.G, &c.B); err != nil || len(s) != 7 {
			return nil, fmt.Errorf("%s's %s is %q, not #RRGGBB", brandPalette, k, s)
		}
		colors[k] = c
	}
	return colors, nil
}

// scriptColor is c as a Plymouth script's red, green and blue arguments, each from 0 to 1.
func scriptColor(c color.RGBA) string {
	return fmt.Sprintf("%.3f, %.3f, %.3f", float64(c.R)/255, float64(c.G)/255, float64(c.B)/255)
}

// scriptQuoted escapes the words for a Plymouth script's double-quoted strings.
func scriptQuoted(w promptWords) promptWords {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	return promptWords{Lang: q(w.Lang), Password: q(w.Password), Caps: q(w.Caps), Retry: q(w.Retry), KeyPIN: q(w.KeyPIN), Touch: q(w.Touch)}
}

// pill is a w by h pill, its ends half circles, filled with fill inside a ringWidth px ring of ring,
// transparent around it. Its edges are anti-aliased by each pixel's distance from them, as the
// shell's entries are, so the curve is not a staircase on the black.
func pill(w, h int, ringWidth float64, fill, ring color.RGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := float64(h) / 2
	coverage := func(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }
	for y := range h {
		for x := range w {
			// The distance from the pixel's centre to the pill's edge: to the segment between the
			// ends' centres, less the radius.
			px := math.Max(r, math.Min(float64(w)-r, float64(x)+.5))
			d := math.Hypot(float64(x)+.5-px, float64(y)+.5-r) - r
			outer, inner := coverage(d), coverage(d+ringWidth)
			if outer == 0 {
				continue
			}
			mix := func(f, g uint8) uint8 {
				return uint8(math.Round((float64(f)*inner + float64(g)*(outer-inner)) / outer))
			}
			img.SetNRGBA(x, y, color.NRGBA{mix(fill.R, ring.R), mix(fill.G, ring.G), mix(fill.B, ring.B), uint8(math.Round(outer * 255))})
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
