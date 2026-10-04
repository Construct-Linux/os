package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repository's own file reads, and has every group the issues use.
func TestTheRepositorysLabels(t *testing.T) {
	labels, err := readLabels("../../.github/labels.json")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, l := range labels {
		have[l.Name] = true
	}
	for _, name := range []string{"bug", "look", "security", "update", "area:kernel", "area:packages", "agent-ready", "severity:critical", "severity:high"} {
		if !have[name] {
			t.Errorf("no %s", name)
		}
	}
}

func TestReadLabelsRefuses(t *testing.T) {
	for name, file := range map[string]string{
		"twice, by case": `[{"group":"type","labels":[{"name":"bug","color":"d73a4a","description":"a"},{"name":"Bug","color":"d73a4a","description":"b"}]}]`,
		"a color with #": `[{"group":"type","labels":[{"name":"bug","color":"#d73a4a","description":"a"}]}]`,
		"no description": `[{"group":"type","labels":[{"name":"bug","color":"d73a4a","description":""}]}]`,
		"too long":       `[{"group":"type","labels":[{"name":"bug","color":"d73a4a","description":"` + strings.Repeat("x", 101) + `"}]}]`,
		"empty":          `[]`,
	} {
		p := filepath.Join(t.TempDir(), "labels.json")
		if err := os.WriteFile(p, []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readLabels(p); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}

// A missing label is created, one that differs - in color or description, or only in its name's
// case - updated, one that matches left alone, and one the file does not list kept.
func TestApplyLabels(t *testing.T) {
	gh := &fakeGitHub{labels: []Label{
		{Name: "Bug", Color: "d73a4a", Description: "Something does not work"},
		{Name: "look", Color: "D876E3", Description: "visual"},
		{Name: "question", Color: "cc317c", Description: "a person's"},
	}}
	want := []Label{
		{Name: "bug", Color: "d73a4a", Description: "Something does not work"},
		{Name: "look", Color: "d876e3", Description: "visual"},
		{Name: "area:boot", Color: "c5def5", Description: "boot"},
	}
	if err := applyLabels(t.Context(), gh, want); err != nil {
		t.Fatal(err)
	}
	if gh.writes != 2 {
		t.Errorf("%d writes, want 2 (bug's case, area:boot)", gh.writes)
	}
	got := map[string]Label{}
	for _, l := range gh.labels {
		got[l.Name] = l
	}
	if _, ok := got["question"]; !ok {
		t.Error("a label the file does not list was deleted")
	}
	if _, ok := got["bug"]; !ok {
		t.Errorf("Bug was not renamed bug: %v", gh.labels)
	}
	if _, ok := got["area:boot"]; !ok {
		t.Error("area:boot was not created")
	}
}

// A dry run writes nothing, and says what it would.
func TestApplyLabelsDryRun(t *testing.T) {
	gh := &fakeGitHub{}
	var out strings.Builder
	if err := applyLabels(t.Context(), logged{gh: gh, out: &out, dry: true}, []Label{{Name: "bug", Color: "d73a4a", Description: "x"}}); err != nil {
		t.Fatal(err)
	}
	if gh.writes != 0 || !strings.Contains(out.String(), "would create label bug") {
		t.Errorf("%d writes; said %q", gh.writes, out.String())
	}
}
