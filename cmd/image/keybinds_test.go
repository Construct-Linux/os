package main

import (
	"os"
	"strings"
	"testing"
)

// keyd's file holds exactly what the table says it must: the Insert chords of copy, paste and
// cut, and the plain key of each default letter; and the table is consistent with itself.
func TestKeydFollowsTheKeysTable(t *testing.T) {
	t.Chdir("../..")
	table, err := readKeysTable("image/rootfs/usr/share/construct/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := table.check(); err != nil {
		t.Fatal(err)
	}
	want, err := table.keydLines()
	if err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile("image/rootfs/etc/keyd/default.conf")
	if err != nil {
		t.Fatal(err)
	}
	if got := keydBindings(string(conf)); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("etc/keyd/default.conf's [meta] layer:\n%s\nkeys.json says:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(table.Apps) < 2 || table.Apps["com.mitchellh.ghostty.desktop"].Name == "" {
		t.Errorf("the terminals are in the table: %v", table.Apps)
	}
}

func TestKeydChord(t *testing.T) {
	for accel, want := range map[string]string{"<Control>Insert": "C-insert", "<Shift>Delete": "S-delete", "<Control><Shift>t": "C-S-t", "F13": "f13"} {
		if got, err := keydChord(accel); err != nil || got != want {
			t.Errorf("%s: %q %v", accel, got, err)
		}
	}
	if _, err := keydChord("<Super>"); err == nil {
		t.Error("an accelerator without a key")
	}
}
