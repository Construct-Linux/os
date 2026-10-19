package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// keysTable is usr/share/construct/keys.json: Super with a letter, the same in every application,
// as keyd and the shell's keys extension do it between them (etc/keyd/default.conf says how). Its
// readers are the extension and construct-keys; here it is checked against keyd's file.
type keysTable struct {
	Everywhere []keyEntry            `json:"everywhere"`
	Default    []keyEntry            `json:"default"`
	Apps       map[string]keysForApp `json:"apps"`
	Shell      []keyEntry            `json:"shell"`
}

type keysForApp struct {
	Name string     `json:"name"`
	Keys []keyEntry `json:"keys"`
}

type keyEntry struct {
	Key   string `json:"key"`
	Plain string `json:"plain"`
	Does  string `json:"does"`
	Sends string `json:"sends"`
}

func readKeysTable(path string) (keysTable, error) {
	var t keysTable
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// keydLines are the bindings keyd's file must hold for the table: the Insert chords of what
// works everywhere, and the plain key of each default letter, in keyd's own spelling.
func (t keysTable) keydLines() ([]string, error) {
	var lines []string
	for _, e := range t.Everywhere {
		chord, err := keydChord(e.Sends)
		if err != nil {
			return nil, err
		}
		lines = append(lines, e.Key+" = "+chord)
	}
	for _, e := range t.Default {
		if e.Plain == "" {
			return nil, fmt.Errorf("default %s: no plain key for keyd to send", e.Key)
		}
		lines = append(lines, e.Key+" = "+strings.ToLower(e.Plain))
	}
	return lines, nil
}

var chordPart = regexp.MustCompile(`<(Control|Shift|Alt)>`)

// keydChord spells a GTK accelerator ("<Control>Insert") as keyd does ("C-insert").
func keydChord(accel string) (string, error) {
	mods := ""
	for _, m := range chordPart.FindAllStringSubmatch(accel, -1) {
		mods += map[string]string{"Control": "C-", "Shift": "S-", "Alt": "A-"}[m[1]]
	}
	key := chordPart.ReplaceAllString(accel, "")
	if key == "" || strings.ContainsAny(key, "<>") {
		return "", fmt.Errorf("%q is no accelerator", accel)
	}
	return mods + strings.ToLower(key), nil
}

// keydBindings are the bindings a keyd file's [meta] layer holds.
func keydBindings(conf string) []string {
	var lines []string
	in := false
	for _, l := range strings.Split(conf, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "["):
			in = l == "[meta]"
		case in && l != "" && !strings.HasPrefix(l, "#"):
			lines = append(lines, l)
		}
	}
	return lines
}

// appKeysCovered is the letters an app's table leaves to the default: every app entry must name a
// default letter, so that the extension never has a letter keyd sends and no chord for it.
func (t keysTable) check() error {
	defaults := map[string]bool{}
	plains := map[string]bool{}
	for _, e := range t.Default {
		defaults[e.Key] = true
		if plains[e.Plain] {
			return fmt.Errorf("default %s: %s is another letter's plain key too", e.Key, e.Plain)
		}
		plains[e.Plain] = true
	}
	for _, e := range t.Everywhere {
		if defaults[e.Key] {
			return fmt.Errorf("%s is everywhere's and default's", e.Key)
		}
	}
	apps := make([]string, 0, len(t.Apps))
	for id := range t.Apps {
		apps = append(apps, id)
	}
	sort.Strings(apps)
	for _, id := range apps {
		for _, e := range t.Apps[id].Keys {
			if !defaults[e.Key] {
				return fmt.Errorf("%s: %s is no default letter, so keyd sends nothing for it", id, e.Key)
			}
			if e.Sends != "" {
				if _, err := keydChord(e.Sends); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
			}
		}
	}
	return nil
}
