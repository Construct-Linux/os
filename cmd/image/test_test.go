package main

import "testing"

// Both units active and rated 2.0 or under; a third line, an inactive unit, a higher score or a
// missing one fails.
func TestSandboxedOK(t *testing.T) {
	for out, want := range map[string]bool{
		"cups-browsed active 1.6\ngeoclue active 1.3\n": true,
		"cups-browsed active 1.6\ngeoclue failed 1.3\n": false,
		"cups-browsed active 9.4\ngeoclue active 1.3\n": false,
		"cups-browsed active\ngeoclue active 1.3\n":     false,
		"cups-browsed active 1.6\n":                     false,
	} {
		if got := sandboxedOK(out); got != want {
			t.Errorf("%q: %v, want %v", out, got, want)
		}
	}
}

// -only runs what it names; a name no scenario answers to is said, not run as nothing.
func TestParseOnly(t *testing.T) {
	want, unknown := parseOnly("views,,panel:wifi,firefox.desktop,panel:nope,typo")
	if len(want) != 5 || !want["views"] || !want["panel:wifi"] || !want["firefox.desktop"] {
		t.Errorf("want %v", want)
	}
	if len(unknown) != 2 || unknown[0] != "panel:nope" || unknown[1] != "typo" {
		t.Errorf("unknown %v", unknown)
	}
	if want, unknown := parseOnly(""); len(want) != 0 || len(unknown) != 0 {
		t.Errorf("all: %v %v", want, unknown)
	}
}
