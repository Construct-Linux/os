package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// The words of the disk's password prompt (plymouth/construct.script) are GNOME's, so every
// language GNOME speaks has them already: the lock screen's "Password" and "Sorry, that didn’t
// work. Please try again." (gnome-shell), and GTK's "Caps Lock is on". A security key's are
// systemd's own, which it asks Plymouth for in English - the initrd has no catalogs - and which
// the script recognises by that text (keyPINPrompt, touchMessage) to show in the person's
// language. English where a catalog lacks one, or where Adwaita Sans - the one font the initrd
// carries - has no glyph for it.
type promptWords struct {
	Lang, Password, Caps, Retry, KeyPIN, Touch string
}

// What systemd-cryptsetup asks Plymouth when /var opens with a security key: the key's PIN, and
// then to touch it (libfido2-util).
const (
	keyPINPrompt = "Please enter security token PIN:"
	touchMessage = "Please confirm presence on security token to unlock."
)

var englishPrompt = promptWords{
	Lang:     "English",
	Password: "Password",
	Caps:     "Caps Lock is on",
	Retry:    "Sorry, that didn’t work. Please try again.",
	KeyPIN:   asTitle(keyPINPrompt),
	Touch:    touchMessage,
}

// promptFont is the font the prompt is written in, in the root and in the initrd.
const promptFont = "usr/share/fonts/Adwaita/AdwaitaSans-Regular.ttf"

// promptSources are the root's files the prompt's words come from: gnome-shell's, GTK's and
// systemd's catalogs, by domain and locale name (es, pt_BR, sr@latin), and the font.
type promptSources struct {
	catalogs map[string]map[string][]byte // domain -> language -> .mo
	font     []byte
}

// add keeps the file name when it is the prompt's font or one of its catalogs.
func (p *promptSources) add(name string, r io.Reader) error {
	if name == promptFont {
		var err error
		p.font, err = io.ReadAll(r)
		return err
	}
	dir, file := path.Split(name)
	lang := strings.TrimSuffix(strings.TrimPrefix(dir, "usr/share/locale/"), "/LC_MESSAGES/")
	if lang == dir || strings.Contains(lang, "/") {
		return nil
	}
	for _, domain := range []string{"gnome-shell", "gtk40", "systemd"} {
		if file == domain+".mo" {
			b, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			if p.catalogs[domain] == nil {
				p.catalogs[domain] = map[string][]byte{}
			}
			p.catalogs[domain][lang] = b
		}
	}
	return nil
}

// languages are the prompt's words in every language gnome-shell has a catalog for in the root
// rootfs, by its locale name. A language is left out unless the font draws all three of its words.
func (p promptSources) languages(rootfs string) (map[string]promptWords, error) {
	catalogs := p.catalogs
	if p.font == nil {
		return nil, fmt.Errorf("%s has no %s", rootfs, promptFont)
	}
	face, err := sfnt.Parse(p.font)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", promptFont, err)
	}
	langs := map[string]promptWords{}
	for lang, shell := range catalogs["gnome-shell"] {
		w := promptWords{Lang: lang}
		var ok bool
		if w.Password, ok = moLookup(shell, englishPrompt.Password); !ok {
			continue
		}
		if w.Retry, ok = moLookup(shell, englishPrompt.Retry); !ok {
			w.Retry = englishPrompt.Retry
		}
		if w.Caps, ok = moLookup(catalogs["gtk40"][lang], englishPrompt.Caps); !ok {
			w.Caps = englishPrompt.Caps
		}
		if !drawable(face, w.Password+w.Caps+w.Retry) {
			continue
		}
		// The key's words come along when systemd has them and the font draws them; the
		// language stays either way, with them in English.
		w.KeyPIN, w.Touch = englishPrompt.KeyPIN, englishPrompt.Touch
		pin, okPIN := moLookup(catalogs["systemd"][lang], keyPINPrompt)
		touch, okTouch := moLookup(catalogs["systemd"][lang], touchMessage)
		if okPIN && okTouch && drawable(face, pin+touch) {
			w.KeyPIN, w.Touch = asTitle(pin), touch
		}
		langs[lang] = w
	}
	return langs, nil
}

// asTitle is a prompt as the title over the field: without the colon that ends it.
func asTitle(prompt string) string {
	return strings.TrimRight(prompt, ":： ")
}

// drawable says whether the font has a glyph for every character of s.
func drawable(f *sfnt.Font, s string) bool {
	var buf sfnt.Buffer
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if i, err := f.GlyphIndex(&buf, r); err != nil || i == 0 {
			return false
		}
	}
	return true
}

// moLookup is msgid's translation in a GNU gettext catalog (.mo), if it has one.
func moLookup(mo []byte, msgid string) (string, bool) {
	if len(mo) < 28 {
		return "", false
	}
	var order binary.ByteOrder = binary.LittleEndian
	switch binary.LittleEndian.Uint32(mo) {
	case 0x950412de:
	case 0xde120495:
		order = binary.BigEndian
	default:
		return "", false
	}
	n, ids, strs := order.Uint32(mo[8:]), order.Uint32(mo[12:]), order.Uint32(mo[16:])
	entry := func(table, i uint32) (string, bool) {
		at := uint64(table) + 8*uint64(i)
		if at+8 > uint64(len(mo)) {
			return "", false
		}
		l, off := uint64(order.Uint32(mo[at:])), uint64(order.Uint32(mo[at+4:]))
		if off+l > uint64(len(mo)) {
			return "", false
		}
		return string(mo[off : off+l]), true
	}
	// The originals are sorted: a binary search finds msgid.
	i := sort.Search(int(n), func(i int) bool {
		s, _ := entry(ids, uint32(i))
		return s >= msgid
	})
	if s, ok := entry(ids, uint32(i)); !ok || s != msgid {
		return "", false
	}
	t, ok := entry(strs, uint32(i))
	if !ok || t == "" {
		return "", false
	}
	// A plural's forms are NUL-separated: the first is the singular.
	t, _, _ = strings.Cut(t, "\x00")
	return t, true
}
