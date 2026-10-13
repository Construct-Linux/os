package main

import (
	"bufio"
	"cmp"
	"os"
	"slices"
	"strings"
	"testing"
)

// apkCompare orders versions as apko does - which picks the build of each package the image gets -
// and refuses what apko refuses: written the same way as tools/apkorder writes apko's own order of
// testdata/apk-versions.txt into testdata/apk-order.golden, which task check makes again with
// apko and compares. apko's module stays out of this one, whose binaries it would otherwise move.
func TestAPKCompareMatchesApko(t *testing.T) {
	f, err := os.Open("testdata/apk-versions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ok, refused []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if _, err := parseAPKVersion(s); err != nil {
			refused = append(refused, s)
		} else {
			ok = append(ok, s)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	compare := func(a, b string) int {
		c, _ := apkCompare(a, b)
		return c
	}
	slices.Sort(refused)
	var b strings.Builder
	for _, s := range refused {
		b.WriteString("refused " + s + "\n")
	}
	slices.SortStableFunc(ok, func(a, b string) int { return cmp.Or(compare(a, b), strings.Compare(a, b)) })
	for i, s := range ok {
		switch {
		case i == 0:
		case compare(ok[i-1], s) == 0:
			b.WriteString(" ")
		default:
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	b.WriteString("\n")
	want, err := os.ReadFile("testdata/apk-order.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range min(len(gl), len(wl)) {
			if gl[i] != wl[i] {
				t.Fatalf("line %d: apkCompare %q, apko %q", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("apkCompare's order has %d lines, apko's %d", len(gl), len(wl))
	}
}
