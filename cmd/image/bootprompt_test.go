package main

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// mo is a GNU gettext catalog of msgid -> translation, as msgfmt writes one.
func mo(entries map[string]string) []byte {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := uint32(len(ids))
	const header = 28
	idTable, strTable := uint32(header), uint32(header)+8*n
	data := strTable + 8*n
	var tables, blob bytes.Buffer
	var strs bytes.Buffer
	put := func(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
	for _, id := range ids {
		put(&tables, uint32(len(id)))
		put(&tables, data+uint32(blob.Len()))
		blob.WriteString(id + "\x00")
	}
	for _, id := range ids {
		t := entries[id]
		put(&strs, uint32(len(t)))
		put(&strs, data+uint32(blob.Len()))
		blob.WriteString(t + "\x00")
	}
	var out bytes.Buffer
	for _, v := range []uint32{0x950412de, 0, n, idTable, strTable, 0, 0} {
		put(&out, v)
	}
	out.Write(tables.Bytes())
	out.Write(strs.Bytes())
	out.Write(blob.Bytes())
	return out.Bytes()
}

func TestACatalogTranslatesWhatItHas(t *testing.T) {
	c := mo(map[string]string{"Password": "Contraseña", "Unlock": "Desbloquear"})
	if s, ok := moLookup(c, "Password"); !ok || s != "Contraseña" {
		t.Errorf("Password -> %q, %v", s, ok)
	}
	if s, ok := moLookup(c, "Caps Lock is on"); ok {
		t.Errorf("an absent msgid translated as %q", s)
	}
}

// A plural entry is found by its singular, as gettext finds it, and answers its first form; an
// entry under a context is not found without it.
func TestAPluralIsFoundByItsSingular(t *testing.T) {
	c := mo(map[string]string{
		"%d file\x00%d files":  "%d archivo\x00%d archivos",
		"%d filer":             "%d archivador",
		"menu\x04Open":         "Abrir",
		"Password\x00Password": "",
	})
	for msgid, want := range map[string]string{"%d file": "%d archivo", "%d filer": "%d archivador", "menu\x04Open": "Abrir"} {
		if s, ok := moLookup(c, msgid); !ok || s != want {
			t.Errorf("%q -> %q, %v; want %q", msgid, s, ok, want)
		}
	}
	for _, msgid := range []string{"Open", "%d files", "Password"} {
		if s, ok := moLookup(c, msgid); ok {
			t.Errorf("%q translated as %q", msgid, s)
		}
	}
}

// A translation whose first form is empty is no translation, whatever the forms after it say: the
// prompt shows the English word, not an empty one.
func TestAnEmptyFirstFormIsUntranslated(t *testing.T) {
	c := mo(map[string]string{"Password": "\x00Contraseñas"})
	if s, ok := moLookup(c, "Password"); ok {
		t.Errorf("Password translated as %q", s)
	}
}

// A language is offered when GNOME's catalogs have its words and the font draws them: Spanish
// with the Go font, which has no Japanese.
func TestALanguageIsOfferedWhenTheFontDrawsIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rootfs.tar")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for name, body := range map[string][]byte{
		promptFont: goregular.TTF,
		"usr/share/locale/es/LC_MESSAGES/gnome-shell.mo": mo(map[string]string{englishPrompt.Password: "Contraseña", englishPrompt.Retry: "Eso no ha funcionado. Inténtelo de nuevo."}),
		"usr/share/locale/es/LC_MESSAGES/gtk40.mo":       mo(map[string]string{englishPrompt.Caps: "Bloq Mayús está activado"}),
		"usr/share/locale/ja/LC_MESSAGES/gnome-shell.mo": mo(map[string]string{englishPrompt.Password: "パスワード"}),
		"usr/share/locale/fr/LC_MESSAGES/gtk40.mo":       mo(map[string]string{englishPrompt.Caps: "Verr. Maj. activé"}),
		"usr/share/locale/es/LC_MESSAGES/systemd.mo":     mo(map[string]string{keyPINPrompt: "Introduzca el PIN del token de seguridad:", touchMessage: "Confirme la presencia en el token de seguridad para desbloquear."}),
		"usr/share/locale/it/LC_MESSAGES/gnome-shell.mo": mo(map[string]string{englishPrompt.Password: "Password"}),
	} {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, prompt, err := readThemeSources(file)
	if err != nil {
		t.Fatal(err)
	}
	langs, err := prompt.languages(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(langs) != 2 || langs["es"].Password != "Contraseña" || langs["es"].Caps != "Bloq Mayús está activado" {
		t.Errorf("languages: %+v", langs)
	}
	// A security key's words are systemd's, the PIN's as a title without its colon; English where
	// systemd has no catalog for the language, which stays offered.
	if es := langs["es"]; es.KeyPIN != "Introduzca el PIN del token de seguridad" || es.Touch != "Confirme la presencia en el token de seguridad para desbloquear." {
		t.Errorf("es key words: %q, %q", es.KeyPIN, es.Touch)
	}
	if it := langs["it"]; it.KeyPIN != "Please enter security token PIN" || it.Touch != touchMessage {
		t.Errorf("it key words: %q, %q", it.KeyPIN, it.Touch)
	}
}
