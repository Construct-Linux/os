package diagnose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// DriverlessPCI are the devices lspci -nnk lists with no "Kernel driver in use" line: hardware
// the kernel has no driver for, or whose driver did not bind.
func DriverlessPCI(lspci string) []string {
	var out []string
	var dev string
	bound := true
	flush := func() {
		if dev != "" && !bound {
			out = append(out, dev)
		}
	}
	for _, l := range strings.Split(lspci, "\n") {
		if l == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			flush()
			dev, bound = l, false
			continue
		}
		if strings.Contains(l, "Kernel driver in use:") {
			bound = true
		}
	}
	flush()
	return out
}

var firmwareFailed = regexp.MustCompile(`(?:Direct firmware load for (\S+) failed|firmware: failed to load (\S+))`)

// MissingFirmware are the files the kernel's log says it asked for and did not find, each once.
func MissingFirmware(kernel string) []string {
	var out []string
	for _, m := range firmwareFailed.FindAllStringSubmatch(kernel, -1) {
		f := m[1] + m[2]
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// entry is what the summary reads of journalctl -o json's records.
type entry struct {
	Unit       string `json:"_SYSTEMD_UNIT"`
	UserUnit   string `json:"_SYSTEMD_USER_UNIT"`
	Identifier string `json:"SYSLOG_IDENTIFIER"`
	Comm       string `json:"_COMM"`
	Message    any    `json:"MESSAGE"`
}

// source is where the record came from: a user unit before the system one it runs under.
func (e entry) source() string {
	for _, s := range []string{e.UserUnit, e.Unit, e.Identifier, e.Comm} {
		if s != "" {
			return s
		}
	}
	return "?"
}

// message is MESSAGE, which journalctl writes as an array of bytes when it is not UTF-8.
func (e entry) message() string {
	switch m := e.Message.(type) {
	case string:
		return m
	case []any:
		var b []byte
		for _, c := range m {
			if f, ok := c.(float64); ok {
				b = append(b, byte(f))
			}
		}
		return string(b)
	}
	return ""
}

// entries are the journal's records, one JSON object a line; a line that is not one is skipped.
func entries(b []byte) []entry {
	var out []entry
	for l := range bytes.SplitSeq(b, []byte("\n")) {
		var e entry
		if json.Unmarshal(l, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// ErrorsByUnit counts journalctl -o json's records by where they came from, the most first, with
// the last message of each: the first n.
func ErrorsByUnit(journal []byte, n int) []string {
	counts := map[string]int{}
	last := map[string]string{}
	for _, e := range entries(journal) {
		counts[e.source()]++
		last[e.source()] = e.message()
	}
	srcs := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	var out []string
	for _, s := range srcs[:min(n, len(srcs))] {
		out = append(out, fmt.Sprintf("%4d  %s: %s", counts[s], s, cut(last[s], 120)))
	}
	return out
}

// authFailed is what a failed login, unlock, sudo, polkit prompt, fingerprint or security key says.
// Not "timed out" alone: D-Bus says it of every service a slow boot did not start in time, and a
// laptop's stick reading at 49 kB/s filled the section with upower, udisks2 and the portals.
var authFailed = regexp.MustCompile(`(?i)authentication failure|auth could not identify|verify-no-match|verify-disconnected|verify-unknown-error|failed to (?:claim|open|verify)|no match|incorrect password|conversation failed`)

// AuthFailures are the records of journalctl -o json that say a login, an unlock, sudo or polkit,
// a fingerprint or a security key failed: the last n.
func AuthFailures(journal []byte, n int) []string {
	var out []string
	for _, e := range entries(journal) {
		if m := e.message(); authFailed.MatchString(m) {
			out = append(out, e.source()+": "+cut(m, 140))
		}
	}
	return out[max(0, len(out)-n):]
}

// Displays are the DRM connectors under root, connected or not, with their modes: a panel the
// GPU's driver does not see is not in Settings either.
func Displays(root string) []byte {
	conns, _ := filepath.Glob(filepath.Join(root, "sys/class/drm/card*-*"))
	var b bytes.Buffer
	for _, c := range conns {
		modes := strings.Fields(text(filepath.Join(c, "modes")))
		fmt.Fprintf(&b, "%s: %s, %d modes %s\n", filepath.Base(c), strings.TrimSpace(text(filepath.Join(c, "status"))),
			len(modes), strings.Join(modes[:min(3, len(modes))], " "))
	}
	return b.Bytes()
}

func text(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// cut is s on one line, cut to n characters: a journal message in a person's language has letters
// of more than one byte, and a cut inside one is not text.
func cut(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
