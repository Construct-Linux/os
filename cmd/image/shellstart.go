package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// shellStart is what the session's shell cost to come up, read from the journal after the fact:
// the seconds since boot its unit started and its startup completed, and each extension's load.
type shellStart struct {
	unit, complete float64
	extensions     []extensionLoad
}

// extensionLoad is one extension's load at the shell's start, from "Loading extension" to the
// state it ended in, in seconds since boot: the shell loads them one after another, so the
// time between is the extension's own.
type extensionLoad struct {
	uuid, state string
	start, end  float64
}

func (e extensionLoad) seconds() float64 { return e.end - e.start }

func (s shellStart) total() float64 { return s.complete - s.unit }

// populated is when the desktop had everything: the last extension's load ended, which comes
// after the startup's completion - the shell completes its startup animation while the
// extensions still load behind it - or that completion when nothing loaded after it.
func (s shellStart) populated() float64 {
	at := s.complete
	for _, e := range s.extensions {
		at = max(at, e.end)
	}
	return at
}

// extensionsTotal is what every extension's load took together.
func (s shellStart) extensionsTotal() float64 {
	t := 0.0
	for _, e := range s.extensions {
		t += e.seconds()
	}
	return t
}

// slowest is the extension whose load took longest; none without extensions.
func (s shellStart) slowest() (extensionLoad, bool) {
	if len(s.extensions) == 0 {
		return extensionLoad{}, false
	}
	return slices.MaxFunc(s.extensions, func(a, b extensionLoad) int {
		switch {
		case a.seconds() < b.seconds():
			return -1
		case a.seconds() > b.seconds():
			return 1
		}
		return 0
	}), true
}

var (
	// systemd 261 logs the unit's description alone ("Starting GNOME Shell..."); earlier ones the
	// unit's name before it. Either way the query is the unit-starting message id's lines only.
	shellUnitStarting = regexp.MustCompile(`systemd\[\d+\]: Starting (org\.gnome\.Shell@\S+ - )?GNOME Shell( on Wayland)?\.\.\.$`)
	shellStarted      = regexp.MustCompile(`gnome-shell\[\d+\]: GNOME Shell started at `)
	extensionLoading  = regexp.MustCompile(`gnome-shell\[\d+\]: Loading extension (\S+)$`)
	extensionLoaded   = regexp.MustCompile(`gnome-shell\[\d+\]: Extension (\S+) in state (\S+) after loading$`)
)

// parseShellStart is the first session's shell start in journal (short-monotonic lines): from the
// last "Starting GNOME Shell..." before "GNOME Shell started at", which the shell logs once its
// startup animation is complete, and every extension that session loaded - before that line or
// after it: the extensions load on the main loop in turns, and the animation completes between
// turns. The greeter's own shell starts the same unit under gdm's user but never logs that it
// started, so its lines are passed over; the extension lines are the extension manager's, logged
// at debug priority (the test layer's G_MESSAGES_DEBUG=GNOME Shell keeps them).
func parseShellStart(journal string) (shellStart, bool) {
	var s shellStart
	started := false
	loading := map[string]float64{}
	for _, line := range strings.Split(journal, "\n") {
		t, ok := monotonic(line)
		if !ok {
			continue
		}
		switch {
		case shellUnitStarting.MatchString(line):
			if s.complete != 0 {
				return s, true
			}
			s = shellStart{unit: t}
			started = true
			clear(loading)
		case !started:
		case shellStarted.MatchString(line):
			s.complete = t
		default:
			if m := extensionLoading.FindStringSubmatch(line); m != nil {
				loading[m[1]] = t
			} else if m := extensionLoaded.FindStringSubmatch(line); m != nil {
				if at, ok := loading[m[1]]; ok {
					s.extensions = append(s.extensions, extensionLoad{uuid: m[1], state: m[2], start: at, end: t})
					delete(loading, m[1])
				}
			}
		}
	}
	return s, s.complete != 0
}

// text is shell-startup.txt: the shell's start and each extension's load, slowest first.
func (s shellStart) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The shell's unit started %.3f s after boot; its startup completed at %.3f s (%.3f s) and the last extension had loaded at %.3f s (%.3f s).\n",
		s.unit, s.complete, s.total(), s.populated(), s.populated()-s.unit)
	if len(s.extensions) == 0 {
		b.WriteString("No extension loads logged: the shell's debug log (G_MESSAGES_DEBUG=GNOME Shell) was off.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\nExtensions, from loading to the state each ended in, %.3f s together:\n", s.extensionsTotal())
	sorted := slices.Clone(s.extensions)
	slices.SortStableFunc(sorted, func(a, b extensionLoad) int {
		switch {
		case a.seconds() > b.seconds():
			return -1
		case a.seconds() < b.seconds():
			return 1
		}
		return 0
	})
	for _, e := range sorted {
		fmt.Fprintf(&b, "%7.0f ms  %-12s %s\n", e.seconds()*1000, e.state, e.uuid)
	}
	return b.String()
}

// shellStartup records what the session's shell took to come up, in the report's line and in
// shell-startup.txt. Not a gate, as boot.txt is not - a VM's numbers are not a machine's - but
// the number a build is compared to the last by: a bar that takes seconds to fill is a
// regression every person sees at every login, and only the extensions' own times say which.
func (r *report) shellStartup(g *guest) {
	journal, err := g.run(`journalctl -b -q --no-pager -o short-monotonic `+
		`MESSAGE_ID=7d4958e842da4a758f6c1cdc7b36dcc5 + MESSAGE_ID=f3ea493c22934e26811cd62abe8e203a + _COMM=gnome-shell PRIORITY=7`, "")
	if err != nil {
		r.add("shell startup", false, "the journal: "+err.Error())
		return
	}
	s, ok := parseShellStart(journal)
	if !ok {
		r.skip("shell startup", "no session's shell started since boot")
		return
	}
	_ = os.WriteFile(filepath.Join(r.dir, "shell-startup.txt"), []byte(s.text()), 0o644)
	detail := fmt.Sprintf("%.2f s from the shell's unit to its startup complete, %.2f s to the last extension loaded", s.total(), s.populated()-s.unit)
	if e, ok := s.slowest(); ok {
		detail += fmt.Sprintf("; %d extensions in %.2f s, the slowest %s in %.0f ms", len(s.extensions), s.extensionsTotal(), e.uuid, e.seconds()*1000)
	}
	r.add("shell startup", true, detail+"; shell-startup.txt")
}
