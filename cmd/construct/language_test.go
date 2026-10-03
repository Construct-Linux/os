package main

import (
	"os"
	"path/filepath"
	"testing"
)

// GNOME's input sources write a layout and its variant as es+winkeys.
func TestTheLanguageIsTheSessionsLocaleAndLayout(t *testing.T) {
	l, err := languageFrom("es_AR.UTF-8", "latam+deadtilde")
	if err != nil {
		t.Fatal(err)
	}
	if string(l.vconsole()) != "XKBLAYOUT=latam\nXKBVARIANT=deadtilde\n" || string(l.localeConf()) != "LANG=es_AR.UTF-8\n" {
		t.Errorf("vconsole.conf %q, locale.conf %q", l.vconsole(), l.localeConf())
	}
	for _, bad := range [][2]string{{"es_AR.UTF-8", "es\nKEYMAP=x"}, {"es AR", "es"}, {"es_AR.UTF-8", "+intl"}} {
		if _, err := languageFrom(bad[0], bad[1]); err == nil {
			t.Errorf("%q, %q taken", bad[0], bad[1])
		}
	}
}

// The boot screen speaks the most specific language there is a theme for, English without one.
func TestTheBootScreenSpeaksTheClosestLanguage(t *testing.T) {
	themes := t.TempDir()
	for _, d := range []string{"spin", "spin-es", "spin-pt_BR", "spin-sr@latin"} {
		if err := os.Mkdir(filepath.Join(themes, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for locale, want := range map[string]string{
		"es_AR.UTF-8": "spin-es", "pt_BR.UTF-8": "spin-pt_BR", "pt_PT.UTF-8": "spin",
		"sr_RS.UTF-8@latin": "spin-sr@latin", "ja_JP.UTF-8": "spin",
	} {
		if got := (language{locale: locale, layout: "us"}).bootTheme(themes); got != want {
			t.Errorf("%s: %s, not %s", locale, got, want)
		}
	}
}

func TestPlymouthIsToldTheTheme(t *testing.T) {
	f := filepath.Join(t.TempDir(), "plymouthd.conf")
	if err := os.WriteFile(f, []byte("[Daemon]\nTheme=spin\nShowDelay=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := plymouthConf(f, "spin-es")
	if err != nil || string(b) != "[Daemon]\nTheme=spin-es\nShowDelay=0\n" {
		t.Errorf("%q %v", b, err)
	}
}
