package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vitalsRecipe = `package:
  name: gnome-shell-extension-vitals
  version: 84

pipeline:
  # v84 plus the fork's fixes.
  - uses: git-checkout
    with:
      repository: https://github.com/Construct-Linux/Vitals
      branch: gnome-51
      expected-commit: f78845bf89b14272ceec46da4d5b370aa9bfee76
`

const brandRecipe = `pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/Construct-Linux/brand
      branch: main
      expected-commit: 32b719201ebc44bf6971c0b875d920cec041bd80
`

const caffeineRecipe = `pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/Construct-Linux/gnome-shell-extension-caffeine.git
      branch: gnome-51
      expected-commit: 29bec135ba6eb5c0f99b1b8eefc7112ac529a349
  - uses: git-checkout
    with:
      repository: https://github.com/eonpatapon/other
      branch: main
      expected-commit: 0000000000000000000000000000000000000000
`

func forkRecipes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"gnome-shell-extension-vitals.yaml": vitalsRecipe, "construct-brand.yaml": brandRecipe, "gnome-shell-extension-caffeine.yaml": caffeineRecipe} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A recipe's git-checkout of a Construct-Linux repository is a fork, with or without .git;
// another owner's is not.
func TestReadForks(t *testing.T) {
	forks, err := readForks(forkRecipes(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range forks {
		names = append(names, f.name+"@"+f.branch+"@"+f.commit[:7])
	}
	if got := strings.Join(names, " "); got != "brand@main@32b7192 gnome-shell-extension-caffeine@gnome-51@29bec13 Vitals@gnome-51@f78845b" {
		t.Errorf("%s", got)
	}
}

func commit(sha, date, subject string, parents ...string) string {
	var ps []string
	for _, p := range parents {
		ps = append(ps, `{"sha": "`+p+`"}`)
	}
	return `{"sha": "` + sha + `", "html_url": "https://github.com/Construct-Linux/Vitals/commit/` + sha + `", "parents": [` + strings.Join(ps, ", ") + `],
		"commit": {"message": "` + subject + `\n\nthe body", "committer": {"date": "` + date + `T10:00:00Z"}}}`
}

// A fork whose pin lacks upstream's commits is an issue listing them, newest first and without
// the merges; one at upstream's head is none; a repository with no parent has no upstream.
func TestForksCheck(t *testing.T) {
	src := serve(t, map[string]string{
		"/repos/Construct-Linux/Vitals": `{"parent": {"full_name": "corecoding/Vitals", "default_branch": "main"}}`,
		"/repos/Construct-Linux/Vitals/compare/f78845bf89b14272ceec46da4d5b370aa9bfee76...corecoding:main": `{"ahead_by": 3, "behind_by": 27,
			"html_url": "https://github.com/Construct-Linux/Vitals/compare/f78845b...corecoding:Vitals:main", "commits": [` +
			commit("753810fb074290841af84e2b4571da32a8704133", "2026-02-25", "Add support to drivetemp kernel module", "a") + `, ` +
			commit("f535963aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "2026-10-12", "Fall back to /proc/cpuinfo | cpufreq", "b") + `, ` +
			commit("cb1c68c0000000000000000000000000000000000", "2026-10-17", "Merge pull request #610 from corecoding/develop", "c", "d") + `]}`,
		"/repos/Construct-Linux/brand":                          `{"parent": null}`,
		"/repos/Construct-Linux/gnome-shell-extension-caffeine": `{"parent": {"full_name": "eonpatapon/gnome-shell-extension-caffeine", "default_branch": "master"}}`,
		"/repos/Construct-Linux/gnome-shell-extension-caffeine/compare/29bec135ba6eb5c0f99b1b8eefc7112ac529a349...eonpatapon:master": `{"ahead_by": 0, "behind_by": 22, "commits": []}`,
	})
	fs, err := src.forksCheck(forkRecipes(t)).find(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("%d findings, want Vitals alone: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.key != "forks:Vitals" || f.title != "update: Vitals is 3 commit(s) behind corecoding/Vitals" || strings.Join(f.labels, ",") != "update,area:desktop" {
		t.Errorf("%q %q %v", f.key, f.title, f.labels)
	}
	cpufreq := strings.Index(f.body, "| 2026-10-12 | [`f535963`](https://github.com/Construct-Linux/Vitals/commit/f535963aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa) | Fall back to /proc/cpuinfo \\| cpufreq |")
	drivetemp := strings.Index(f.body, "| 2026-02-25 | [`753810f`]")
	if cpufreq < 0 || drivetemp < 0 || cpufreq > drivetemp {
		t.Errorf("the commits, newest first:\n%s", f.body)
	}
	if strings.Contains(f.body, "Merge pull request") || !strings.Contains(f.body, "27 commit(s) of its own") || !strings.Contains(f.body, "corecoding/Vitals's main has 3 commit(s)") ||
		!strings.Contains(f.body, "gnome-shell-extension-vitals.yaml") || !strings.Contains(f.body, "rebase gnome-51 onto main") {
		t.Errorf("the body:\n%s", f.body)
	}
}

// Upstream not answering for one fork fails the check, which leaves every fork's issue as it is.
func TestForksCheckUpstreamDown(t *testing.T) {
	src := serve(t, map[string]string{"/repos/Construct-Linux/brand": `{"parent": null}`})
	_, err := src.forksCheck(forkRecipes(t)).find(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Vitals") {
		t.Errorf("Vitals unanswered: %v", err)
	}
}
