package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Construct-Linux/os/internal/grype"
)

// Commits group by the area their subject starts with, the busiest area first; a subject without
// one is "other".
func TestByArea(t *testing.T) {
	areas, by := byArea([]string{
		"gdm: a slow display's timeout quits Plymouth with the transition",
		"harness: a greeter in front of the live session fails the run",
		"gdm: another",
		"Merge the thing nobody named",
	})
	if strings.Join(areas, ",") != "gdm,harness,other" {
		t.Errorf("areas %v", areas)
	}
	if len(by["gdm"]) != 2 || by["other"][0] != "Merge the thing nobody named" {
		t.Errorf("by %v", by)
	}
}

// The package diff names what was added, removed and updated, each sorted.
func TestDiffPackages(t *testing.T) {
	added, removed, updated := diffPackages(
		map[string]string{"gdm": "51.0-r2", "mesa": "26.2.1-r0", "cantarell": "0.303-r1"},
		map[string]string{"gdm": "51.0-r3", "mesa": "26.2.1-r0", "nss-mdns": "0.15.1-r0"})
	if len(added) != 1 || added[0].Name != "nss-mdns" || len(removed) != 1 || removed[0].Name != "cantarell" ||
		len(updated) != 1 || updated[0] != (pkgChange{"gdm", "51.0-r2", "51.0-r3"}) {
		t.Errorf("added %v removed %v updated %v", added, removed, updated)
	}
}

// apk's database gives each package its version.
func TestParseInstalled(t *testing.T) {
	got, err := parseInstalled(strings.NewReader("P:gdm\nV:51.0-r3\nA:x86_64\n\nP:mesa\nV:26.2.1-r0\n"))
	if err != nil || got["gdm"] != "51.0-r3" || got["mesa"] != "26.2.1-r0" || len(got) != 2 {
		t.Errorf("%v %v", got, err)
	}
}

// The notes carry each section a release needs, a first release without a package diff.
func TestWriteNotes(t *testing.T) {
	var g grype.Report
	m := grype.Match{}
	m.Vulnerability.ID, m.Vulnerability.Severity = "CVE-2026-1", "High"
	m.Vulnerability.Fix.Versions = []string{"3.5.1-r0"}
	m.Artifact.Name, m.Artifact.Version = "openssl", "3.5.0-r0"
	g.Matches = []grype.Match{m}
	in := notesInput{
		Version: "20261004.1", Date: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		Commits:  []string{"gdm: x"},
		Packages: map[string]string{"linux": "7.2.8-r0", "gnome-shell": "51.0-r1"},
		Grype:    &g, Ours: map[string]bool{},
		Tests:      []testRun{{Name: "test", Passed: 450, Total: 454, Failed: []string{"suspend and resume"}}},
		UpdatesURL: "https://updates.example",
	}
	var b strings.Builder
	writeNotes(&b, in)
	s := b.String()
	for _, want := range []string{"# CONSTRUCT 20261004.1", "kernel 7.2.8-r0", "The first release", "**gdm**",
		"| CVE-2026-1 | High | openssl | 3.5.0-r0 | 3.5.1-r0 |", "450 of 454", "suspend and resume",
		"https://updates.example/usb/construct_20261004.1.raw.xz", "gpg --verify SHA256SUMS.gpg"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in:\n%s", want, s)
		}
	}
}
