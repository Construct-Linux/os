package forks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const recipe = `package:
  name: gnome-shell-extension-dash-to-panel
  version: "74"
  epoch: 8

pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/Construct-Linux/dash-to-panel
      branch: gnome-51
      expected-commit: 7e0a33a0be5fe84ce40b0b7660e0111ffedaf86e
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A recipe's git-checkout of a Construct-Linux repository is a pin, with the package's version
// and epoch; another owner's checkout is none, and a recipe is found by its name or the fork's.
func TestReadAndFind(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "gnome-shell-extension-dash-to-panel.yaml", recipe)
	write(t, dir, "bibata-cursor-theme.yaml", "package:\n  name: bibata-cursor-theme\n  version: 2.0.7\n  epoch: 4\npipeline:\n  - uses: git-checkout\n    with:\n      repository: https://github.com/Construct-Linux/Bibata_Cursor.git\n      branch: gnome-51\n      expected-commit: 2e382aac7957772311a6f0a36e429cdd00f7959b\n")
	write(t, dir, "other.yaml", "package:\n  name: other\n  version: 1\n  epoch: 0\npipeline:\n  - uses: git-checkout\n    with:\n      repository: https://github.com/someone/other\n      branch: main\n      expected-commit: 0000000000000000000000000000000000000000\n")
	pins, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].Name != "Bibata_Cursor" || pins[0].Version != "2.0.7" || pins[0].Epoch != 4 ||
		pins[1].Name != "dash-to-panel" || pins[1].Version != "74" || pins[1].Epoch != 8 || pins[1].Branch != "gnome-51" || pins[1].Commit[:7] != "7e0a33a" {
		t.Errorf("%+v", pins)
	}
	for _, name := range []string{"dash-to-panel", "gnome-shell-extension-dash-to-panel", "gnome-shell-extension-dash-to-panel.yaml"} {
		if p, err := Find(dir, name); err != nil || p.Name != "dash-to-panel" {
			t.Errorf("Find(%q): %+v, %v", name, p, err)
		}
	}
	if _, err := Find(dir, "nothing"); err == nil {
		t.Error("an unknown name found something")
	}
}

// Move rewrites the commit, the version with its quotes and the epoch, and refuses a pin that
// moves the commit alone.
func TestMove(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "gnome-shell-extension-dash-to-panel.yaml", recipe)
	p, err := Find(dir, "dash-to-panel")
	if err != nil {
		t.Fatal(err)
	}
	head := "de94e38af6519485b9ddceac821586c1f144d92d"
	if err := p.Move(head, "74", 8); err == nil {
		t.Error("the commit alone moved")
	}
	if err := p.Move(head, "75", 0); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.Recipe)
	s := string(b)
	if !strings.Contains(s, "  version: \"75\"\n  epoch: 0\n") || !strings.Contains(s, "expected-commit: "+head) || strings.Contains(s, p.Commit) {
		t.Errorf("after Move:\n%s", s)
	}
	moved, _ := Find(dir, "dash-to-panel")
	if moved.Version != "75" || moved.Epoch != 0 || moved.Commit != head {
		t.Errorf("%+v", moved)
	}
}
