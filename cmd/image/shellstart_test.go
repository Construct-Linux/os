package main

import (
	"strings"
	"testing"
)

// The person's session is read, not the greeter's shell before it (the same unit, which never
// logs that it started); each extension's load is its own time, before or after the startup
// completed; and a later session's start ends the reading.
func TestParseShellStart(t *testing.T) {
	journal := strings.Join([]string{
		"[    4.100000] construct systemd[400]: Starting GNOME Shell...",
		"[    4.500000] construct gnome-shell[420]: Loading extension dash-to-panel@jderose9.github.com",
		"[    4.600000] construct gnome-shell[420]: Extension dash-to-panel@jderose9.github.com in state INITIALIZED after loading",
		"[    8.900000] construct systemd[700]: Starting GNOME Shell screencast service...",
		"[    9.000000] construct systemd[700]: Starting GNOME Shell...",
		"[    9.700000] construct gnome-shell[750]: Loading extension dash-to-panel@jderose9.github.com",
		"[    9.700500] construct gnome-shell[750]: Changing state of extension dash-to-panel@jderose9.github.com to ACTIVE",
		"[   10.100000] construct gnome-shell[750]: Extension dash-to-panel@jderose9.github.com in state ACTIVE after loading",
		"[   10.100000] construct gnome-shell[750]: Loading extension Vitals@CoreCoding.com",
		"[   10.200000] construct gnome-shell[750]: GNOME Shell started at Sat Oct 18 2026 14:50:52 GMT-0300 (Argentina Standard Time)",
		"[   10.350000] construct gnome-shell[750]: Extension Vitals@CoreCoding.com in state ERROR after loading",
		"[   20.000000] construct systemd[900]: Starting org.gnome.Shell@wayland.service - GNOME Shell on Wayland...",
		"[   20.500000] construct gnome-shell[950]: Loading extension Vitals@CoreCoding.com",
		"[   21.000000] construct gnome-shell[950]: Extension Vitals@CoreCoding.com in state ACTIVE after loading",
	}, "\n")
	s, ok := parseShellStart(journal)
	if !ok {
		t.Fatal("no shell start found")
	}
	if s.unit != 9 || s.complete != 10.2 || s.populated() != 10.35 {
		t.Errorf("unit %.3f complete %.3f populated %.3f, want 9, 10.2 and 10.35", s.unit, s.complete, s.populated())
	}
	if len(s.extensions) != 2 {
		t.Fatalf("extensions: %+v", s.extensions)
	}
	if e := s.extensions[0]; e.uuid != "dash-to-panel@jderose9.github.com" || e.state != "ACTIVE" || e.seconds() < 0.399 || e.seconds() > 0.401 {
		t.Errorf("dash-to-panel: %+v", e)
	}
	if e := s.extensions[1]; e.state != "ERROR" || e.seconds() < 0.249 || e.seconds() > 0.251 {
		t.Errorf("Vitals: %+v", e)
	}
	if e, _ := s.slowest(); e.uuid != "dash-to-panel@jderose9.github.com" {
		t.Errorf("slowest: %+v", e)
	}
	text := s.text()
	if !strings.Contains(text, "(1.200 s)") || !strings.Contains(text, "(1.350 s)") || !strings.Contains(text, "0.650 s together") ||
		!strings.Contains(text, "dash-to-panel@jderose9.github.com (at 0.70 s)") ||
		strings.Index(text, "dash-to-panel") > strings.Index(text, "Vitals") {
		t.Errorf("text:\n%s", text)
	}
}

// A boot whose session never completed its startup - the greeter alone, a shell that hung - has
// no number; one without the debug log has the shell's number and no extension's.
func TestParseShellStartMissing(t *testing.T) {
	if _, ok := parseShellStart("[    4.100000] construct systemd[400]: Starting GNOME Shell...\n"); ok {
		t.Error("a shell that never started was read as one that did")
	}
	s, ok := parseShellStart(strings.Join([]string{
		"[    9.000000] construct systemd[700]: Starting GNOME Shell...",
		"[   11.500000] construct gnome-shell[750]: GNOME Shell started at Sat Oct 18 2026 14:50:52 GMT-0300",
	}, "\n"))
	if !ok || s.total() != 2.5 || s.populated() != 11.5 || len(s.extensions) != 0 || !strings.Contains(s.text(), "No extension loads logged") {
		t.Errorf("ok %v, %+v:\n%s", ok, s, s.text())
	}
}
