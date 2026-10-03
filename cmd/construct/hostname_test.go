package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestValidHostname(t *testing.T) {
	for in, want := range map[string]string{
		"construct":             "construct",
		" Ada-X9 ":              "ada-x9",
		"a":                     "a",
		"x9-15p":                "x9-15p",
		strings.Repeat("a", 63): strings.Repeat("a", 63),
	} {
		got, err := validHostname(in)
		if err != nil || got != want {
			t.Errorf("validHostname(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-ada", "ada-", "ada.local", "ada lovelace", "ñandú", strings.Repeat("a", 64)} {
		if _, err := validHostname(in); err == nil {
			t.Errorf("validHostname(%q): no error", in)
		}
	}
}

// An empty line is the default; a name that is none is asked again.
func TestAskHostname(t *testing.T) {
	got, err := askHostname(bufio.NewReader(strings.NewReader("\n")), io.Discard)
	if err != nil || got != defaultHostname {
		t.Errorf("empty line: %q, %v", got, err)
	}
	got, err = askHostname(bufio.NewReader(strings.NewReader("bad name\nAda-X9\n")), io.Discard)
	if err != nil || got != "ada-x9" {
		t.Errorf("after a bad name: %q, %v", got, err)
	}
}
