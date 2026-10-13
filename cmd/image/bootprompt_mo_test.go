package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// moEntry is one message of a catalog: its context, original, plural and translations.
type moEntry struct {
	ctx, id, plural string
	strs            []string
}

// key is the entry's original as a .mo stores it and as moLookup is asked for it: under a context,
// the context and an EOT before it; a plural's after a NUL.
func (e moEntry) key() string {
	k := e.id
	if e.ctx != "" {
		k = e.ctx + "\x04" + k
	}
	return k
}

// moTestEntries are the shapes of message a desktop's catalogs hold: the prompt's own words,
// characters past ASCII, quotes and escapes, a plural, a context, a word that has both, and one
// nobody translated.
var moTestEntries = []moEntry{
	{id: "", strs: []string{"Content-Type: text/plain; charset=UTF-8\nPlural-Forms: nplurals=2; plural=(n != 1);\n"}},
	{id: "Password", strs: []string{"Contraseña"}},
	{id: englishPrompt.Caps, strs: []string{"Bloq Mayús está activado"}},
	{id: englishPrompt.Retry, strs: []string{"Eso no ha funcionado. Inténtelo de nuevo."}},
	{id: keyPINPrompt, strs: []string{"Introduzca el PIN del token de seguridad:"}},
	{id: touchMessage, strs: []string{"Confirme la presencia en el token de seguridad para desbloquear."}},
	{id: "%d file", plural: "%d files", strs: []string{"%d archivo", "%d archivos"}},
	{ctx: "menu", id: "Open", strs: []string{"Abrir"}},
	{id: "Open", strs: []string{"Abrir sin contexto"}},
	{id: "Say \"hi\"\tnow \\", strs: []string{"Di \"hola\"\tahora \\"}},
	{id: "パスワード", strs: []string{"パスワード (ja)"}},
	{id: "Untranslated", strs: []string{""}},
}

// handMO is moTestEntries as mo, the hand-assembled catalog, writes them.
func handMO() []byte {
	m := map[string]string{}
	for _, e := range moTestEntries {
		k := e.key()
		if e.plural != "" {
			k += "\x00" + e.plural
		}
		m[k] = strings.Join(e.strs, "\x00")
	}
	return mo(m)
}

// po is moTestEntries as a .po file, for msgfmt.
func po() string {
	var b strings.Builder
	for _, e := range moTestEntries {
		if e.ctx != "" {
			fmt.Fprintf(&b, "msgctxt %s\n", strconv.Quote(e.ctx))
		}
		fmt.Fprintf(&b, "msgid %s\n", strconv.Quote(e.id))
		if e.plural != "" {
			fmt.Fprintf(&b, "msgid_plural %s\n", strconv.Quote(e.plural))
			for i, s := range e.strs {
				fmt.Fprintf(&b, "msgstr[%d] %s\n", i, strconv.Quote(s))
			}
		} else {
			fmt.Fprintf(&b, "msgstr %s\n", strconv.Quote(e.strs[0]))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// msgfmtMO is moTestEntries compiled by GNU msgfmt, in the byte order asked, or nil without it.
func msgfmtMO(t testing.TB, endianness string) []byte {
	t.Helper()
	if _, err := exec.LookPath("msgfmt"); err != nil {
		return nil
	}
	dir := t.TempDir()
	src, out := filepath.Join(dir, "test.po"), filepath.Join(dir, "test.mo")
	if err := os.WriteFile(src, []byte(po()), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("msgfmt", "--endianness="+endianness, "-o", out, src).CombinedOutput(); err != nil {
		t.Fatalf("msgfmt: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// msgunfmt is what GNU msgunfmt reads in catalog: each message's lookup key and the translation
// gettext answers for it, the first form of a plural's.
func msgunfmt(t *testing.T, catalog []byte) map[string]string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "test.mo")
	if err := os.WriteFile(f, catalog, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("msgunfmt", "--no-wrap", f).Output()
	if err != nil {
		t.Fatalf("msgunfmt: %v", err)
	}
	got := map[string]string{}
	var ctx, id, str, unused string
	var seen bool
	var cur *string // the field a continued string adds to
	flush := func() {
		if seen {
			k := id
			if ctx != "" {
				k = ctx + "\x04" + id
			}
			got[k] = str
		}
		ctx, id, str, seen, cur = "", "", "", false, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		quoted := line
		switch {
		case line == "":
			flush()
			continue
		case strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, `"`):
			// A string continued on the next line, as msgunfmt writes one with newlines.
		default:
			var keyword string
			keyword, quoted, _ = strings.Cut(line, " ")
			switch keyword {
			case "msgctxt":
				cur = &ctx
			case "msgid":
				cur, seen = &id, true
			case "msgstr", "msgstr[0]":
				cur = &str
			default:
				// msgid_plural and the other plural forms, which gettext() does not answer.
				cur = &unused
			}
		}
		s, err := strconv.Unquote(quoted)
		if err != nil || cur == nil {
			t.Fatalf("msgunfmt wrote %q", line)
		}
		*cur += s
	}
	flush()
	return got
}

// moLookup answers, for every message of a catalog, the translation GNU gettext's own reader
// (msgunfmt) finds in it, for catalogs msgfmt wrote in either byte order and for the tests'
// hand-assembled one: the prompt's words are read from the root's catalogs with it, and a word
// it misreads is a prompt in the wrong language.
func TestMoLookupReadsWhatMsgunfmtReads(t *testing.T) {
	if _, err := exec.LookPath("msgunfmt"); err != nil {
		t.Skip("no msgunfmt")
	}
	catalogs := map[string][]byte{"hand-assembled": handMO()}
	if c := msgfmtMO(t, "little"); c != nil {
		catalogs["msgfmt, little-endian"] = c
		catalogs["msgfmt, big-endian"] = msgfmtMO(t, "big")
	}
	for name, c := range catalogs {
		want := msgunfmt(t, c)
		// Every translated message is there, so the parse above missed none; msgfmt leaves the
		// untranslated out.
		for _, e := range moTestEntries {
			if _, ok := want[e.key()]; !ok && e.strs[0] != "" {
				t.Fatalf("%s: msgunfmt read no %q: %q", name, e.key(), want)
			}
		}
		for k, w := range want {
			if k == "" {
				continue // the header, which gettext answers for no word
			}
			s, ok := moLookup(c, k)
			if ok != (w != "") || s != w {
				t.Errorf("%s: moLookup(%q) = %q, %v; msgunfmt reads %q", name, k, s, ok, w)
			}
		}
	}
}

// bigEndian is a little-endian catalog in the other byte order, as msgfmt --endianness=big
// writes it: the header's words and both tables'.
func bigEndian(c []byte) []byte {
	b := bytes.Clone(c)
	n := binary.LittleEndian.Uint32(c[8:])
	words := 7 + 4*int(n)
	for i := range words {
		binary.BigEndian.PutUint32(b[4*i:], binary.LittleEndian.Uint32(c[4*i:]))
	}
	return b
}

// A catalog in the root is a package's file, read at build: whatever its bytes, moLookup answers
// a translation found inside them, or none, and never panics or reads past them.
func FuzzMoLookup(f *testing.F) {
	hand := handMO()
	seeds := [][]byte{hand, bigEndian(hand), msgfmtMO(f, "little"), msgfmtMO(f, "big")}
	for _, c := range seeds {
		if c == nil {
			continue
		}
		for _, e := range moTestEntries {
			f.Add(c, e.key())
		}
		// Cut short in the header, the tables and the strings.
		for _, n := range []int{27, 28, 40, len(c) / 2, len(c) - 1} {
			f.Add(c[:n], "Password")
		}
	}
	f.Fuzz(func(t *testing.T, catalog []byte, msgid string) {
		s, ok := moLookup(catalog, msgid)
		if !ok {
			if s != "" {
				t.Fatalf("not found, yet answered %q", s)
			}
			return
		}
		if s == "" || strings.Contains(s, "\x00") || !bytes.Contains(catalog, []byte(s)) {
			t.Fatalf("moLookup(%q) = %q, which is no translation in the catalog", msgid, s)
		}
	})
}
