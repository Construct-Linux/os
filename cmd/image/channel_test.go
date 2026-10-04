package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseSums is a release's SHA256SUMS: its root, verity and kernel image.
func releaseSums(v string) string {
	h := strings.Repeat("a", 64)
	return fmt.Sprintf("%s  construct_%s_11111111-2222-3333-4444-555555555555.root.raw.xz\n"+
		"%s  construct_%s_66666666-7777-8888-9999-000000000000.verity.raw.xz\n%s  construct_%s.efi\n", h, v, h, v, h, v)
}

// Versions compare as systemd-sysupdate compares them: a day's tenth build is newer than its
// ninth, and the next day's first newer than both.
func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"20261004.10", "20261004.9", true},
		{"20261005.1", "20261004.10", true},
		{"20261004.1", "20261004.1", false},
		{"20261004.2", "20261004.150701", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// A channel's index gains a release, keeps the newest versions, and loses a withdrawn one; a
// release with a missing file is refused.
func TestChannelIndex(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ch := filepath.Join(dir, "testing")
	for i, v := range []string{"20261004.1", "20261004.9", "20261004.10", "20261005.1"} {
		rel := write("rel-"+v, releaseSums(v))
		from := []string{"-current", filepath.Join(ch, "SHA256SUMS")}
		if i == 0 {
			from = []string{"-new"}
		}
		if err := channelIndex(append(from, "-add", rel, "-version", v, "-keep", "3", "-o", ch)); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(ch, "SHA256SUMS"))
	idx, err := parseIndex(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(idx.versions(), " "); got != "20261005.1 20261004.10 20261004.9" {
		t.Errorf("kept %s", got)
	}
	if !strings.HasPrefix(string(b), strings.Repeat("a", 64)+"  construct_20261005.1") {
		t.Errorf("the newest is not first:\n%s", b)
	}
	if err := channelIndex([]string{"-current", filepath.Join(ch, "SHA256SUMS"), "-withdraw", "-version", "20261005.1", "-o", ch}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(ch, "SHA256SUMS"))
	if idx, _ := parseIndex(string(b)); idx["20261005.1"] != nil || len(idx) != 2 {
		t.Errorf("after the withdrawal: %v", idx.versions())
	}
	short := write("short", strings.Join(strings.Split(releaseSums("20261006.1"), "\n")[:2], "\n"))
	if err := channelIndex([]string{"-new", "-add", short, "-version", "20261006.1", "-o", ch}); err == nil {
		t.Error("a release without its kernel image: no error")
	}
	// An index that could not be fetched is not a new channel: only -new starts one.
	rel := write("rel-20261007.1", releaseSums("20261007.1"))
	for _, from := range [][]string{{"-current", filepath.Join(dir, "missing")}, {}, {"-new", "-current", filepath.Join(ch, "SHA256SUMS")}} {
		if err := channelIndex(append(from, "-add", rel, "-version", "20261007.1", "-o", ch)); err == nil {
			t.Errorf("%v: no error", from)
		}
	}
	if _, err := parseIndex(strings.Repeat("a", 64) + "  evil.sh\n"); err == nil {
		t.Error("an index naming another file: no error")
	}
}
