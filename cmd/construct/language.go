package main

import (
	"github.com/Construct-Linux/os/internal/program"

	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// language is the language and keyboard layout a machine is installed with: its person's, as the
// live session the installer runs in has them. The installed /etc gets them (locale.conf,
// vconsole.conf), and so does the initrd, through credentials on the ESP, so the disk's password
// is asked in that language and typed with that layout (image/initrd's
// construct-boot-language.service).
type language struct {
	locale  string // LANG: es_AR.UTF-8
	layout  string // XKB layout: es
	variant string // XKB variant: winkeys, or none
}

var (
	localeName = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
	xkbName    = regexp.MustCompile(`^[a-z0-9_-]*$`)
)

// languageFrom is the language the flags name - a locale, and a layout as GNOME's input sources
// write it (es+winkeys) - or, without them, the running system's.
func languageFrom(locale, keyboard string) (language, error) {
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	if locale == "" {
		locale = "en_US.UTF-8"
	}
	l := language{locale: locale}
	if keyboard == "" {
		keyboard = systemKeyboard("/etc/vconsole.conf")
	}
	l.layout, l.variant, _ = strings.Cut(keyboard, "+")
	if !localeName.MatchString(l.locale) || l.layout == "" || !xkbName.MatchString(l.layout) || !xkbName.MatchString(l.variant) {
		return language{}, fmt.Errorf("not a locale and keyboard layout: %q, %q", locale, keyboard)
	}
	return l, nil
}

// systemKeyboard is the layout vconsole.conf names, us without one.
func systemKeyboard(file string) string {
	b, _ := os.ReadFile(file)
	layout, variant := "us", ""
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "XKBLAYOUT":
			layout = strings.Trim(v, `"`)
		case "XKBVARIANT":
			variant = strings.Trim(v, `"`)
		}
	}
	if variant != "" {
		return layout + "+" + variant
	}
	return layout
}

// vconsole is vconsole.conf for l: the XKB layout, which Plymouth and GNOME read keys with.
func (l language) vconsole() []byte {
	s := "XKBLAYOUT=" + l.layout + "\n"
	if l.variant != "" {
		s += "XKBVARIANT=" + l.variant + "\n"
	}
	return []byte(s)
}

// localeConf is locale.conf for l.
func (l language) localeConf() []byte { return []byte("LANG=" + l.locale + "\n") }

// bootTheme is the Plymouth theme that speaks l's language, of those under themes (cmd/image
// plymouth.go makes construct-<locale> for each it can): the most specific of es_AR, es and, for a
// locale with a modifier, sr_RS@latin and sr@latin. construct, which speaks English, without one.
func (l language) bootTheme(themes string) string {
	name, _, _ := strings.Cut(l.locale, ".")
	modifier := ""
	if i := strings.IndexByte(l.locale, '@'); i >= 0 {
		modifier = l.locale[i:]
		name, _, _ = strings.Cut(name, "@")
	}
	lang, _, _ := strings.Cut(name, "_")
	for _, c := range []string{name + modifier, lang + modifier, name, lang} {
		if _, err := os.Stat(filepath.Join(themes, "construct-"+c)); err == nil {
			return "construct-" + c
		}
	}
	return "construct"
}

// plymouthConf is the running root's plymouthd.conf with theme in place of its own.
func plymouthConf(file, theme string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "Theme=") {
			line = "Theme=" + theme
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

// bootCredentials are the credentials the new ESP carries for the initrd
// (loader/credentials/<name>.cred), encrypted as systemd takes credentials from the ESP: to the
// TPM, with no PCR, where there is one - only this machine opens them - and with the null key
// where there is none, which systemd accepts only then.
func bootCredentials(ctx context.Context, l language, tpm bool) (map[string][]byte, error) {
	conf, err := plymouthConf("/etc/plymouth/plymouthd.conf", l.bootTheme("/usr/share/plymouth/themes"))
	if err != nil {
		return nil, err
	}
	key := []string{"--with-key=null"}
	if tpm {
		key = []string{"--with-key=tpm2", "--tpm2-device=auto", "--tpm2-pcrs="}
	}
	creds := map[string][]byte{}
	for name, body := range map[string][]byte{"construct.vconsole": l.vconsole(), "construct.plymouth": conf} {
		var enc bytes.Buffer
		args := append([]string{"encrypt", "--name=" + name}, key...)
		if err := (program.With{Stdin: body, Stdout: &enc}).Run(ctx, "systemd-creds", append(args, "-", "-")...); err != nil {
			return nil, err
		}
		creds[name] = enc.Bytes()
	}
	return creds, nil
}
